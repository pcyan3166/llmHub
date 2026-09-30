package llmhub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureCatalog(server *Server, input *string) {
	server.catalog.fetch = func(ctx context.Context, url string) ([]byte, error) {
		if raw := fixtureNews(url); raw != nil {
			return raw, nil
		}
		if strings.Contains(url, "deepseek") {
			return []byte(deepSeekPage(*input)), nil
		}
		if strings.Contains(url, "claude.com") {
			return []byte(anthropicPage()), nil
		}
		return []byte(openAIPage(*input)), nil
	}
}

func TestCatalogApprovalAutomaticUpdateAndFrozenBilling(t *testing.T) {
	s, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
	})
	s.now = func() time.Time { return instant("2026-09-30T12:00:00Z") }
	input := "1"
	fixtureCatalog(s, &input)
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	state, _ := store.catalogState()
	if len(state.Sources) != 3 || len(state.Events) != 7 {
		t.Fatalf("initial discovery: %+v", state)
	}
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := store.catalogState()
	if len(duplicate.Events) != len(state.Events) {
		t.Fatal("duplicate event spam")
	}
	config, version, _ := store.Config()
	if config.Profiles[0].InputUSDPerMillion != 1 || version != 2 || config.Profiles[0].FollowOfficial {
		t.Fatal("manual prices changed")
	}
	source, _ := catalogQuote(state, config.Profiles[0])
	approve := func(hash string, v int64) int {
		raw, _ := json.Marshal(map[string]any{"profile_id": "text-fast", "hash": hash, "version": v, "confirm_conditions": true})
		return call(s, "POST", "/api/llmhub/catalog/apply", testAdmin, string(raw)).Code
	}
	if approve(source.Hash, 1) != 409 {
		t.Fatal("stale config proposal accepted")
	}
	if approve(strings.Repeat("a", 64), 2) != 409 {
		t.Fatal("stale page proposal accepted")
	}
	if approve(source.Hash, 2) != 200 {
		t.Fatal("approval failed")
	}
	if w := call(s, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"x"}]}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before, _ := store.Recent("storepilot", 1)
	if before[0].PriceSnapshot.AppliedPrice.OfficialSourceHash != source.Hash {
		t.Fatal("source proof missing in billing snapshot")
	}
	input = "2"
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	config, version, _ = store.Config()
	if version != 4 || config.Profiles[0].InputUSDPerMillion != 2 || config.Profiles[0].MaxOutputTokens != 32 {
		t.Fatal("auto update failed or damaged request limits", config, version)
	}
	state, _ = store.catalogState()
	if s.catalog.profileStatus(state, config.Profiles[0]).Status != "verified" {
		t.Fatal("applied profile not verified")
	}
	if len(s.snapshot().Profiles) != 1 || len(s.snapshot().Scenes[0].Profiles) != 1 {
		t.Fatal("new model silently joined routes")
	}
	after, _ := store.Recent("storepilot", 1)
	if after[0].CostUSD != before[0].CostUSD || after[0].PriceSnapshot.InputUSDPerMillion != 1 {
		t.Fatal("historical invoice repriced")
	}
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	_, v, _ := store.Config()
	if v != version {
		t.Fatal("unchanged quotes churned config version")
	}
	fixture := s.catalog.fetch
	s.catalog.fetch = func(ctx context.Context, url string) ([]byte, error) {
		if raw := fixtureNews(url); raw != nil {
			return raw, nil
		}
		raw, err := fixture(ctx, url)
		return append(raw, []byte("\nNew surcharge conditions.")...), err
	}
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	if w := call(s, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"x"}]}`); w.Code != 503 || !strings.Contains(w.Body.String(), "official_price_unverified") {
		t.Fatal("unapproved new billing terms did not stop dispatch", w.Code, w.Body.String())
	}
	s.catalog.fetch = func(context.Context, string) ([]byte, error) { return nil, io.ErrUnexpectedEOF }
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	state, _ = store.catalogState()
	source, _ = catalogQuote(state, config.Profiles[0])
	if source.Error == "" || source.Quotes[0].Input != 2 {
		t.Fatal("failed fetch erased last known prices")
	}
	if w := call(s, "GET", "/api/llmhub/catalog", "", ""); w.Code != 401 {
		t.Fatal("catalog authentication bypass")
	}
	if w := call(s, "GET", "/api/llmhub/catalog", testAdmin, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "stale") {
		t.Fatal(w.Body.String())
	}
}

func TestCatalogSettingsLifecycleBackoffAndConcurrentEdits(t *testing.T) {
	s, store, _ := testServer(t, func(http.ResponseWriter, *http.Request) {})
	input := "1"
	fixtureCatalog(s, &input)
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	s.catalog.fetch = func(ctx context.Context, url string) ([]byte, error) {
		if strings.Contains(url, "openai") {
			config, version, _ := store.Config()
			config.Projects[0].Name = "Concurrent edit"
			raw, _ := json.Marshal(map[string]any{"config": config, "version": version})
			if w := call(s, "PUT", "/api/llmhub/config", testAdmin, string(raw)); w.Code != 200 {
				t.Error(w.Body.String())
			}
		}
		if strings.Contains(url, "deepseek") {
			return []byte(deepSeekPage("9")), nil
		}
		if strings.Contains(url, "claude.com") {
			return []byte(anthropicPage()), nil
		}
		return []byte(openAIPage("9")), nil
	}
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	config, _, _ := store.Config()
	if config.Projects[0].Name != "Concurrent edit" || config.Profiles[0].InputUSDPerMillion != 1 {
		t.Fatal("admin edit overwritten")
	}
	config.Catalog = &CatalogSettings{Enabled: false, IntervalMinutes: 15}
	s.configMu.Lock()
	s.config = config
	s.configMu.Unlock()
	var calls atomic.Int64
	s.catalog.fetch = func(context.Context, string) ([]byte, error) { calls.Add(1); return nil, io.EOF }
	if err := s.catalog.check(false); err != nil || calls.Load() != 0 {
		t.Fatal("disabled worker fetched")
	}
	config.Catalog.Enabled = true
	if err := s.catalog.check(false); err != nil || calls.Load() != 0 {
		t.Fatal("restart ignored persisted next-check deadline")
	}
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	state, _ := store.catalogState()
	source := state.Sources[0]
	if sourceDue(source, *config.Catalog).Sub(source.CheckedAt) != 5*time.Minute {
		t.Fatal("no failure backoff")
	}
	if err := s.catalog.check(false); err != nil || calls.Load() != 6 {
		t.Fatal("tight retry loop")
	}
	s.catalog.fetch = func(ctx context.Context, url string) ([]byte, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if !s.catalog.kick(true) || s.catalog.kick(true) {
		t.Fatal("single-flight check failed")
	}
	if w := call(s, "POST", "/api/llmhub/catalog/check", testAdmin, ""); w.Code != 429 {
		t.Fatal("manual check not throttled")
	}
	s.catalog.close()
	if s.catalog.kick(true) {
		t.Fatal("closed worker restarted")
	}
	for _, minutes := range []int{0, 14, 10081} {
		bad := testConfig()
		bad.Catalog = &CatalogSettings{IntervalMinutes: minutes}
		if bad.Validate() == nil {
			t.Fatal("bad polling interval accepted")
		}
	}
}

func TestOfficialPeakTariffPreservesCalendarAndNeedsAnnualApproval(t *testing.T) {
	quotes, _, err := parseCatalog("deepseek", []byte(deepSeekPage("2")))
	if err != nil {
		t.Fatal(err)
	}
	p := testConfig().Profiles[0]
	p.Provider = "deepseek"
	p.Pricing = &PriceSchedule{Timezone: "UTC", CalendarTimezone: "Asia/Shanghai", DefaultLabel: "谷时", ExcludedDates: []string{"2026-10-01"}, Windows: []PriceWindow{
		{ID: "morning", Label: "峰时", Days: []int{1, 2, 3, 4, 5}, Start: "01:00", End: "04:00"},
		{ID: "afternoon", Label: "峰时", Days: []int{1, 2, 3, 4, 5}, Start: "06:00", End: "10:00"},
	}}
	at := instant("2026-09-30T02:00:00Z")
	next, err := officialTariff(p, quotes[0], at, true)
	if err != nil || next.InputUSDPerMillion != 2 || next.Pricing.Windows[0].InputUSDPerMillion != 4 || next.OfficialCalendarYear != 2026 {
		t.Fatal(next, err)
	}
	if p.Pricing.Windows[0].InputUSDPerMillion != 0 || next.Pricing.ExcludedDates[0] != "2026-10-01" {
		t.Fatal("mutated shared schedule or calendar")
	}
	if next.priceAt(instant("2026-10-01T02:00:00Z")).InputUSDPerMillion != 2 {
		t.Fatal("holiday tariff lost")
	}
	if _, err := officialTariff(next, quotes[0], instant("2027-01-01T00:00:00Z"), false); err == nil {
		t.Fatal("calendar silently trusted in next year")
	}
	next.Pricing.ExcludedDates = []string{"2026-10-02"}
	if _, err := officialTariff(next, quotes[0], at, false); err == nil {
		t.Fatal("edited holiday calendar remained approved")
	}
	p.Pricing.Windows[0].Start = "02:00"
	if _, err := officialTariff(p, quotes[0], at, true); err == nil {
		t.Fatal("incompatible peak schedule accepted")
	}
}

func fixtureNews(url string) []byte {
	if strings.Contains(url, "changelog") {
		return []byte("# Changelog\nReleased `gpt-test-news`.")
	}
	if strings.Contains(url, "models/overview") {
		return []byte("# Models overview\nAPI ID `claude-test-news`.")
	}
	if strings.Contains(url, "/updates") {
		return []byte("<article><h1>Change Log</h1><p>Released deepseek-test-news.</p></article>")
	}
	return nil
}

func TestCatalogHTTPBoundsAndURLAllowlist(t *testing.T) {
	s, _, _ := testServer(t, func(http.ResponseWriter, *http.Request) {})
	c := s.catalog
	if _, err := c.fetchOfficial(context.Background(), "http://127.0.0.1/private"); err == nil {
		t.Fatal("SSRF allowlist bypass")
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect was followed") }))
	defer target.Close()
	for _, mode := range []string{"redirect", "large", "status", "good"} {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				t.Error("credentials leaked")
			}
			switch mode {
			case "redirect":
				http.Redirect(w, r, target.URL, 302)
			case "large":
				io.WriteString(w, strings.Repeat("x", (2<<20)+1))
			case "status":
				w.WriteHeader(429)
			default:
				io.WriteString(w, "okay")
			}
		}))
		base := http.DefaultTransport.(*http.Transport).Clone()
		c.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			copy := r.Clone(r.Context())
			u := *r.URL
			u.Scheme = "http"
			u.Host = strings.TrimPrefix(mock.URL, "http://")
			copy.URL = &u
			return base.RoundTrip(copy)
		})
		_, err := c.fetchOfficial(context.Background(), officialSources[0].URL)
		mock.Close()
		base.CloseIdleConnections()
		if (err == nil) != (mode == "good") {
			t.Fatal(mode, err)
		}
	}
}

func TestCatalogStateSurvivesRestartAndAtomicApplyRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	if _, err := store.SaveConfig(config, 1); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(store, "http://127.0.0.1:1", testAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	input := "1"
	fixtureCatalog(s, &input)
	if err := s.catalog.check(true); err != nil {
		t.Fatal(err)
	}
	state, _ := store.catalogState()
	source, q := catalogQuote(state, config.Profiles[0])
	config.Profiles[0].FollowOfficial = true
	config.Profiles[0].OfficialTerms = source.TermsHash
	if _, err := store.SaveConfig(config, 2); err != nil {
		t.Fatal(err)
	}
	s.Close()
	store.Close()
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, err = NewServer(store, "http://127.0.0.1:1", testAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, _ := store.catalogState()
	if len(loaded.Events) != len(state.Events) || loaded.Sources[0].Hash != state.Sources[0].Hash {
		t.Fatal("lost catalog after restart")
	}
	var calls atomic.Int64
	s.catalog.fetch = func(context.Context, string) ([]byte, error) { calls.Add(1); return nil, io.EOF }
	s.StartCatalog(context.Background())
	s.StartCatalog(context.Background())
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.catalog.mu.Lock()
		done := !s.catalog.running
		s.catalog.mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 0 {
		t.Fatal("restart caused duplicate network fetch")
	}
	s.catalog.close()
	if _, err := store.db.Exec("CREATE TRIGGER fail_catalog_update BEFORE UPDATE ON catalog_state BEGIN SELECT RAISE(FAIL, 'injected catalog failure'); END;"); err != nil {
		t.Fatal(err)
	}
	config.Profiles[0].InputUSDPerMillion = q.Input + 1
	if _, err := store.saveConfigAndCatalog(config, 3, &state); err == nil {
		t.Fatal("injected failure did not fail")
	}
	unchanged, version, _ := store.Config()
	if version != 3 || unchanged.Profiles[0].InputUSDPerMillion != 1 {
		t.Fatal("partial price update without catalog audit committed")
	}
	var history int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM config_history WHERE version=4").Scan(&history); err != nil || history != 0 {
		t.Fatal("partial config history committed")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOfficialVerificationAtQueueDispatchAndApprovedFallback(t *testing.T) {
	for _, mode := range []string{"queued_change", "expired_fallback"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			s, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
			})
			at := instant("2026-09-30T12:00:00Z")
			s.now = func() time.Time { return at }
			input := "1"
			fixtureCatalog(s, &input)
			if err := s.catalog.check(true); err != nil {
				t.Fatal(err)
			}
			state, _ := store.catalogState()
			source, _ := catalogQuote(state, s.snapshot().Profiles[0])
			raw, _ := json.Marshal(map[string]any{"profile_id": "text-fast", "hash": source.Hash, "version": 2, "confirm_conditions": true})
			if w := call(s, "POST", "/api/llmhub/catalog/apply", testAdmin, string(raw)); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if mode == "expired_fallback" {
				config, version, _ := store.Config()
				p := config.Profiles[0]
				p.ID = "manual-backup"
				p.FollowOfficial = false
				config.Profiles = append(config.Profiles, p)
				config.Scenes[0].Profiles = append(config.Scenes[0].Profiles, p.ID)
				raw, _ = json.Marshal(map[string]any{"config": config, "version": version})
				if w := call(s, "PUT", "/api/llmhub/config", testAdmin, string(raw)); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				at = at.Add(13 * time.Hour)
				w := call(s, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"hello"}]}`)
				if w.Code != 200 || w.Header().Get("X-LLMHub-Profile") != "manual-backup" || calls.Load() != 1 {
					t.Fatal(w.Code, w.Body.String(), calls.Load())
				}
				return
			}
			hold, err := s.scheduler.Acquire(context.Background(), "shared", "hold", 1)
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Finish(0)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- call(s, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"hello"}]}`)
			}()
			deadline := time.Now().Add(time.Second)
			queued := false
			for time.Now().Before(deadline) {
				for _, pool := range s.scheduler.Stats() {
					if pool.Queued == 1 {
						queued = true
					}
				}
				if queued {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !queued {
				t.Fatal("request did not queue")
			}
			input = "2"
			if err := s.catalog.check(true); err != nil {
				t.Fatal(err)
			}
			hold.Finish(0)
			select {
			case w := <-done:
				if w.Code != 503 || calls.Load() != 0 {
					t.Fatal("stale queued quote dispatched", w.Code, calls.Load())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("queued request did not finish")
			}
			usage, _ := store.Usage("storepilot", at.Format("2006-01"))
			if len(usage) != 0 {
				t.Fatal("unverified dispatch reserved budget")
			}
		})
	}
}
