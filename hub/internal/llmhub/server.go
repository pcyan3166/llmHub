package llmhub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Demo            bool // Set before serving by the local mock executable only.
	store           *Store
	scheduler       *Scheduler
	client          *http.Client
	upstream        *url.URL
	adminHash       [32]byte
	bifrostUser     string
	bifrostPassword string
	configMu        sync.RWMutex
	config          Config
	version         int64
	admission       chan struct{}
	staticDir       string
	logger          *slog.Logger
	now             func() time.Time
	catalog         *Catalog
}

func NewServer(store *Store, upstream, adminToken, staticDir string) (*Server, error) {
	if len(adminToken) < 24 {
		return nil, fmt.Errorf("LLMHUB_ADMIN_TOKEN must contain at least 24 characters")
	}
	u, err := url.Parse(upstream)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("upstream must be an HTTP origin without credentials or a path")
	}
	c, version, err := store.Config()
	if err != nil {
		return nil, err
	}
	if err = c.Validate(); err != nil {
		return nil, err
	}
	scheduler, err := NewScheduler(store, c.Pools)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 64, MaxIdleConnsPerHost: 32, MaxConnsPerHost: 256, IdleConnTimeout: 60 * time.Second, ResponseHeaderTimeout: 90 * time.Second}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s := &Server{store: store, scheduler: scheduler, client: client, upstream: u, adminHash: sha256.Sum256([]byte(adminToken)), config: c, version: version, admission: make(chan struct{}, 128), staticDir: staticDir, logger: slog.Default(), now: time.Now}
	s.bifrostUser, s.bifrostPassword = os.Getenv("LLMHUB_BIFROST_USERNAME"), os.Getenv("LLMHUB_BIFROST_PASSWORD")
	s.catalog = newCatalog(s)
	return s, nil
}

func (s *Server) Close() { s.catalog.close(); s.scheduler.Close(); s.client.CloseIdleConnections() }

func (s *Server) snapshot() Config { s.configMu.RLock(); defer s.configMu.RUnlock(); return s.config }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/health" && r.Method == "GET" {
		if err := s.store.db.PingContext(r.Context()); err != nil {
			writeError(w, 503, "storage_unavailable", "storage is unavailable")
		} else {
			writeJSON(w, 200, map[string]string{"status": "ok", "service": "llmHub"})
		}
		return
	}
	if r.URL.Path == "/ready" && r.Method == "GET" {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", s.upstream.String()+"/health", nil)
		resp, err := s.client.Do(req)
		if err != nil {
			writeError(w, 503, "bifrost_unavailable", "Bifrost is unavailable")
			return
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			writeError(w, 503, "bifrost_unavailable", "Bifrost health check failed")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/llmhub/") {
		if !s.isAdmin(r) {
			writeError(w, 401, "unauthorized", "admin token is required")
			return
		}
		s.admin(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		s.inference(w, r)
		return
	}
	if r.Method == "GET" && s.staticDir != "" {
		path := filepath.Join(s.staticDir, filepath.Clean("/"+r.URL.Path))
		if r.URL.Path == "/" {
			path = filepath.Join(s.staticDir, "index.html")
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'")
			http.ServeFile(w, r, path)
			return
		}
	}
	writeError(w, 404, "not_found", "route not found")
}

func bearer(r *http.Request) string {
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func (s *Server) isAdmin(r *http.Request) bool {
	hash := sha256.Sum256([]byte(bearer(r)))
	return subtle.ConstantTimeCompare(hash[:], s.adminHash[:]) == 1
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"type": "llmhub_error", "code": code, "message": message}})
}
func readJSON(w http.ResponseWriter, r *http.Request, value any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON document")
	}
	return nil
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/llmhub/catalog") {
		s.catalog.admin(w, r)
		return
	}
	switch {
	case r.URL.Path == "/api/llmhub/estimate" && r.Method == "POST":
		s.estimate(w, r)
	case r.URL.Path == "/api/llmhub/config" && r.Method == "GET":
		s.configMu.RLock()
		defer s.configMu.RUnlock()
		writeJSON(w, 200, map[string]any{"config": s.config, "version": s.version})
	case r.URL.Path == "/api/llmhub/config" && r.Method == "PUT":
		s.saveConfiguration(w, r)
	case r.URL.Path == "/api/llmhub/keys" && r.Method == "GET":
		keys, err := s.store.Keys()
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"keys": keys})
	case r.URL.Path == "/api/llmhub/pricing" && r.Method == "GET":
		at := s.now()
		profiles := []Profile{}
		verification := []CatalogProfile{}
		state, err := s.store.catalogState()
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, p := range s.snapshot().Profiles {
			profiles = append(profiles, p.priceAt(at))
			verification = append(verification, s.catalog.profileStatus(state, p))
		}
		writeJSON(w, 200, map[string]any{"at": at.UTC(), "profiles": profiles, "verification": verification})
	case r.URL.Path == "/api/llmhub/keys" && r.Method == "POST":
		var body struct {
			ProjectID string `json:"project_id"`
		}
		if err := readJSON(w, r, &body); err != nil {
			writeError(w, 400, "invalid_request", err.Error())
			return
		}
		if p, ok := s.snapshot().Project(body.ProjectID); !ok || !p.Enabled {
			writeError(w, 400, "invalid_project", "an enabled project is required")
			return
		}
		key, token, err := s.store.CreateKey(body.ProjectID)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, 201, map[string]any{"key": key, "token": token})
	case strings.HasPrefix(r.URL.Path, "/api/llmhub/keys/") && r.Method == "DELETE":
		id := strings.TrimPrefix(r.URL.Path, "/api/llmhub/keys/")
		if err := s.store.RevokeKey(id); err != nil {
			writeError(w, 404, "not_found", "key not found")
			return
		}
		w.WriteHeader(204)
	case r.URL.Path == "/api/llmhub/overview" && r.Method == "GET":
		month := r.URL.Query().Get("month")
		if month == "" {
			month = time.Now().UTC().Format("2006-01")
		}
		if _, err := time.Parse("2006-01", month); err != nil {
			writeError(w, 400, "invalid_month", "month must use YYYY-MM")
			return
		}
		usage, err := s.store.Usage(r.URL.Query().Get("project"), month)
		if err != nil {
			s.fail(w, err)
			return
		}
		recent, err := s.store.Recent(r.URL.Query().Get("project"), 100)
		if err != nil {
			s.fail(w, err)
			return
		}
		breakdown, err := s.store.Breakdown(r.URL.Query().Get("project"), month)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"month": month, "usage": usage, "recent": recent, "breakdown": breakdown, "pools": s.scheduler.Stats(), "pending": len(s.admission), "pending_limit": cap(s.admission), "demo": s.Demo})
	default:
		writeError(w, 404, "not_found", "admin route not found")
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logger.Error("llmHub storage failure", "error", err)
	writeError(w, 503, "storage_unavailable", "storage is unavailable")
}

func (s *Server) inference(w http.ResponseWriter, r *http.Request) {
	projectID, err := s.store.Authenticate(bearer(r))
	if err != nil {
		writeError(w, 401, "unauthorized", "a valid project key is required")
		return
	}
	c := s.snapshot()
	project, ok := c.Project(projectID)
	if !ok || !project.Enabled {
		writeError(w, 403, "project_disabled", "project is disabled")
		return
	}
	if r.URL.Path == "/v1/models" && r.Method == "GET" {
		data := []map[string]string{}
		for _, scene := range c.Scenes {
			if scene.ProjectID == projectID {
				data = append(data, map[string]string{"id": "scene/" + scene.ID, "object": "model", "owned_by": "llmHub"})
			}
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": data})
		return
	}
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/responses" && r.URL.Path != "/v1/embeddings" && r.URL.Path != "/v1/images/generations" {
		writeError(w, 404, "not_found", "inference route not found")
		return
	}
	select {
	case s.admission <- struct{}{}:
		defer func() { <-s.admission }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "hub_busy", "llmHub pending request limit reached")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		writeError(w, 413, "request_too_large", "request body exceeds 256 KiB")
		return
	}
	var selector struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &selector) != nil {
		writeError(w, 400, "invalid_request", "invalid JSON body")
		return
	}
	sceneID := r.Header.Get("X-LLMHub-Scene")
	alias := strings.TrimPrefix(selector.Model, "scene/")
	if sceneID == "" {
		sceneID = alias
	} else if selector.Model != "" && alias != sceneID {
		writeError(w, 400, "scene_mismatch", "model and X-LLMHub-Scene must identify the same scene")
		return
	}
	scene, ok := c.Scene(projectID, sceneID)
	if !ok || scene.Endpoint != r.URL.Path {
		writeError(w, 403, "scene_unavailable", "scene is not available to this project on this endpoint")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(scene.TimeoutSeconds)*time.Second)
	defer cancel()
	requestID := randomID("req_")
	w.Header().Set("X-Request-ID", requestID)
	remaining := append([]string(nil), scene.Profiles...)
	attemptsUsed := make(map[string]int, len(remaining))
	for len(remaining) > 0 {
		valid := remaining[:0]
		for _, id := range remaining {
			if _, err := s.catalog.verifiedProfile(c.Profile(id)); err == nil {
				valid = append(valid, id)
			}
		}
		remaining = valid
		if len(remaining) == 0 {
			writeError(w, 503, "official_price_unverified", "官方报价已过期或待核对，跟随官方的档案暂不派发请求")
			return
		}
		profileID := cheapestRoute(c, scene, remaining, raw, s.now())
		profile, credentialSource := c.resolvedProfile(projectID, profileID)
		reroute := false
		body, input, output, stream, err := prepareRequest(raw, scene.Endpoint, profile)
		if err != nil {
			writeError(w, 400, "invalid_request", err.Error())
			return
		}
		for retry := attemptsUsed[profileID]; retry <= scene.Retries; retry++ {
			if ctx.Err() != nil {
				writeError(w, 504, "request_timeout", "request deadline exceeded")
				return
			}
			attemptID := randomID("attempt_")
			queuedAt := time.Now()
			queueCtx, queueCancel := context.WithTimeout(ctx, time.Duration(scene.QueueTimeoutSeconds)*time.Second)
			lease, err := s.scheduler.Acquire(queueCtx, profile.PoolID, attemptID, input+output)
			queueCancel()
			if err != nil {
				if errors.Is(err, ErrTokens) {
					writeError(w, 400, "tpm_reservation_too_large", err.Error())
					return
				}
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					writeError(w, 504, "queue_timeout", "request timed out while waiting for the shared queue")
					return
				}
				if errors.Is(err, ErrQueueFull) {
					w.Header().Set("Retry-After", "1")
					writeError(w, 429, "queue_full", err.Error())
					return
				}
				s.fail(w, err)
				return
			}
			queueMS := time.Since(queuedAt).Milliseconds()
			pricedAt := s.now()
			if cheapestRoute(c, scene, remaining, raw, pricedAt) != profileID {
				lease.Finish(0)
				reroute = true
				break
			}
			source, priceErr := s.catalog.verifiedProfile(c.Profile(profileID))
			if priceErr != nil {
				lease.Finish(0)
				writeError(w, 503, "official_price_unverified", priceErr.Error())
				return
			}
			profile, _ = c.resolvedProfile(projectID, profileID)
			profile = profile.priceAt(pricedAt)
			profile.AppliedPrice.CredentialSource = credentialSource
			if source != nil {
				profile.AppliedPrice.OfficialSourceURL = source.URL
				profile.AppliedPrice.OfficialSourceHash = source.Hash
				verified := source.VerifiedAt
				profile.AppliedPrice.OfficialVerifiedAt = &verified
			}
			attempt := Attempt{ID: attemptID, RequestID: requestID, ProjectID: projectID, SceneID: scene.ID, ProfileID: profile.ID, PoolID: profile.PoolID, Provider: profile.Provider, Model: profile.Model, InputTokens: input, OutputTokens: output, QueueMS: queueMS, PriceSnapshot: &profile}
			reserve := costMicros(profile, input, output)
			attempt.Prediction, err = s.store.predict(projectID, scene.ID, scene.Endpoint, s.upstream.String(), profile, body, input, output, pricedAt)
			if err != nil {
				lease.Finish(0)
				s.fail(w, err)
				return
			}
			// Recheck the project after waiting so disabling it immediately stops queued traffic.
			current, exists := s.snapshot().Project(projectID)
			if !exists || !current.Enabled {
				lease.Finish(0)
				writeError(w, 403, "project_disabled", "project is disabled")
				return
			}
			if _, err = s.store.Authenticate(bearer(r)); err != nil {
				lease.Finish(0)
				writeError(w, 401, "key_revoked", "project key was revoked while queued")
				return
			}
			latest, latestSource := s.snapshot().resolvedProfile(projectID, profileID)
			if latest.KeyName != profile.KeyName || latest.PoolID != profile.PoolID || latestSource != credentialSource {
				lease.Finish(0)
				writeError(w, 409, "credentials_changed", "upstream credentials changed while queued; submit a new request")
				return
			}
			if err = s.store.Reserve(attempt, current.MonthlyBudgetUSD, reserve); err != nil {
				lease.Finish(0)
				if errors.Is(err, ErrBudget) {
					writeError(w, 402, "budget_exhausted", err.Error())
				} else {
					s.fail(w, err)
				}
				return
			}
			startedAt := time.Now()
			attemptsUsed[profileID]++
			nativeCache := profile.Provider == "deepseek" && scene.Endpoint == "/v1/chat/completions"
			prefix := "/openai"
			if nativeCache {
				prefix = ""
			}
			req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(s.upstream.String(), "/")+prefix+scene.Endpoint, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-BF-API-Key", profile.KeyName)
			req.Header.Set("X-Request-ID", requestID)
			if s.bifrostUser != "" {
				req.SetBasicAuth(s.bifrostUser, s.bifrostPassword)
			}
			resp, callErr := s.client.Do(req)
			if callErr != nil {
				lease.Finish(input + output)
				s.settle(attemptID, "upstream_error", 502, input, output, reserve, startedAt, true)
				// Transport failures may have billed work; retrying them risks duplicate execution.
				writeError(w, 502, "bifrost_unavailable", "Bifrost request failed")
				return
			}
			if resp.StatusCode == 429 {
				until := retryAfter(resp.Header.Get("Retry-After"), time.Now(), retry)
				s.scheduler.Cooldown(profile.PoolID, until)
			}
			if resp.StatusCode == 429 || resp.StatusCode == 503 {
				if retry < scene.Retries || len(remaining) > 1 {
					_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
					resp.Body.Close()
					lease.Finish(input + output)
					s.settle(attemptID, "retry", resp.StatusCode, 0, 0, 0, startedAt, true)
					if resp.StatusCode == 503 {
						s.scheduler.Cooldown(profile.PoolID, time.Now().Add(time.Duration(1<<retry)*time.Second))
					}
					continue
				}
			}
			w.Header().Set("X-LLMHub-Profile", profile.ID)
			w.Header().Set("X-LLMHub-Model", profile.Provider+"/"+profile.Model)
			w.Header().Set("X-LLMHub-Queue-MS", strconv.FormatInt(queueMS, 10))
			w.Header().Set("X-LLMHub-Price-Window", profile.AppliedPrice.WindowID)
			if stream && resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
				s.stream(w, resp, lease, attemptID, profile, input, output, reserve, startedAt, nativeCache)
				return
			}
			responseRaw, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
			resp.Body.Close()
			if readErr != nil || len(responseRaw) > 8<<20 {
				lease.Finish(input + output)
				s.settle(attemptID, "response_error", 502, input, output, reserve, startedAt, true)
				writeError(w, 502, "invalid_upstream_response", "upstream response could not be read within the 8 MiB limit")
				return
			}
			usage := parseUsage(responseRaw)
			if nativeCache {
				responseRaw = normalizeCacheUsage(responseRaw)
				usage = parseUsage(responseRaw)
			}
			if output > 0 && !usage.outputKnown {
				usage.known = false
			}
			estimated := usageEstimated(profile, usage)
			if usage.known {
				input, output = usage.input, usage.output
				if usage.cacheWrite == 0 {
					reserve = usageCostMicros(profile, usage)
				}
				profile.AppliedPrice.CachedInputTokens = usage.cached
				profile.AppliedPrice.CacheUsageKnown = usage.cachedKnown
				profile.AppliedPrice.UsageKnown = true
				profile.AppliedPrice.CacheWriteTokens = usage.cacheWrite
				profile.AppliedPrice.CacheWriteUsageKnown = usage.cacheWriteKnown
			} else if resp.StatusCode >= 400 {
				input, output, reserve = 0, 0, 0
			}
			if scene.Endpoint == "/v1/images/generations" && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Image tariffs are operator-supplied estimates, independent of text token pricing.
				input, output, reserve, estimated = 0, 0, costMicros(profile, 0, 0), true
			}
			lease.Finish(func() int64 {
				if usage.known {
					return input + output
				}
				return lease.event.tokens
			}())
			status := "success"
			if resp.StatusCode >= 400 {
				status = "error"
			}
			s.settle(attemptID, status, resp.StatusCode, input, output, reserve, startedAt, estimated, &profile)
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				w.Header().Set("Content-Type", ct)
			} else {
				w.Header().Set("Content-Type", "application/json")
			}
			if v := resp.Header.Get("Retry-After"); v != "" {
				w.Header().Set("Retry-After", v)
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(responseRaw)
			return
		}
		if !reroute {
			for i, id := range remaining {
				if id == profileID {
					remaining = append(remaining[:i], remaining[i+1:]...)
					break
				}
			}
		}
	}
	writeError(w, 503, "routes_exhausted", "all scene profiles are unavailable")
}

func (s *Server) settle(id, status string, httpStatus int, input, output, cost int64, started time.Time, estimated bool, snapshots ...*Profile) {
	if err := s.store.Settle(id, status, httpStatus, input, output, cost, time.Since(started).Milliseconds(), estimated, snapshots...); err != nil {
		s.logger.Error("usage settlement failed; reservation retained", "attempt_id", id, "error", err)
	}
}

func (s *Server) stream(w http.ResponseWriter, resp *http.Response, lease *Lease, id string, p Profile, input, output, reserve int64, started time.Time, nativeCache bool) {
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	controller := http.NewResponseController(w)
	_ = controller.Flush()
	tracker := usageTracker{}
	var reader io.Reader = resp.Body
	if nativeCache {
		reader = newCacheUsageReader(resp.Body)
	}
	buffer := make([]byte, 16<<10)
	status := "success"
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			tracker.Write(buffer[:n])
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				status = "canceled"
				break
			}
			if flushErr := controller.Flush(); flushErr != nil {
				status = "canceled"
				break
			}
		}
		if err != nil {
			if err != io.EOF {
				status = "stream_error"
			}
			break
		}
	}
	tracker.consume()
	if output > 0 && !tracker.usage.outputKnown {
		tracker.usage.known = false
	}
	if status == "success" && (!tracker.terminal || tracker.failed) {
		status = "stream_error"
	}
	estimated := usageEstimated(p, tracker.usage) || status != "success"
	if tracker.usage.known && status == "success" {
		input, output = tracker.usage.input, tracker.usage.output
		if tracker.usage.cacheWrite == 0 {
			reserve = usageCostMicros(p, tracker.usage)
		}
		p.AppliedPrice.CachedInputTokens = tracker.usage.cached
		p.AppliedPrice.CacheUsageKnown = tracker.usage.cachedKnown
		p.AppliedPrice.UsageKnown = true
		p.AppliedPrice.CacheWriteTokens = tracker.usage.cacheWrite
		p.AppliedPrice.CacheWriteUsageKnown = tracker.usage.cacheWriteKnown
	}
	lease.Finish(input + output)
	s.settle(id, status, resp.StatusCode, input, output, reserve, started, estimated, &p)
}

func retryAfter(value string, now time.Time, retry int) time.Time {
	if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 0 {
		if n > 3600 {
			n = 3600
		}
		return now.Add(time.Duration(n) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		if date.After(now.Add(time.Hour)) {
			return now.Add(time.Hour)
		}
		return date
	}
	return now.Add(time.Duration(1<<retry) * time.Second)
}
