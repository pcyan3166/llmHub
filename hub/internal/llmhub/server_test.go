package llmhub

import (
	"bytes"
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

const testAdmin = "test-admin-token-at-least-24-characters"

func testConfig() Config {
	return Config{
		Projects: []Project{{ID: "storepilot", Name: "StorePilot", Enabled: true, MonthlyBudgetUSD: 10}},
		Pools:    []Pool{{ID: "shared", Concurrency: 1, QueueSize: 4, RPM: 100, TPM: 100000}},
		Profiles: []Profile{{ID: "text-fast", Provider: "openai", Model: "test-model", KeyName: "primary", PoolID: "shared", MaxOutputTokens: 32, InputUSDPerMillion: 1, OutputUSDPerMillion: 2}},
		Scenes:   []Scene{{ID: "copy", ProjectID: "storepilot", Name: "Copy", Profiles: []string{"text-fast"}, Endpoint: "/v1/chat/completions", TimeoutSeconds: 3, QueueTimeoutSeconds: 2}},
	}
}

func TestProjectUpstreamCredentialsDispatchPredictionAndNoFallback(t *testing.T) {
	var keys []string
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-BF-API-Key")
		keys = append(keys, key)
		if key == "broken" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"bad key"}}`)
			return
		}
		if strings.Contains(r.URL.RawQuery, "stream") {
			t.Fatal("unexpected query")
		}
		io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	})
	server.now = func() time.Time { return time.Now().Add(10 * time.Second) }
	update := func(binding []ProviderCredential) {
		t.Helper()
		c, v, err := store.Config()
		if err != nil {
			t.Fatal(err)
		}
		c.DefaultCredentials = []ProviderCredential{{Provider: "openai", KeyName: "global", PoolID: "shared"}}
		found := false
		for _, p := range c.Pools {
			if p.ID == "dedicated" {
				found = true
			}
		}
		if !found {
			c.Pools = append(c.Pools, Pool{ID: "dedicated", Concurrency: 1, RPM: 100, TPM: 100000})
		}
		c.Projects[0].Credentials = binding
		raw, _ := json.Marshal(map[string]any{"config": c, "version": v})
		if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(raw)); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	request := `{"model":"copy","messages":[{"role":"user","content":"same"}]}`
	update(nil)
	if w := call(server, "POST", "/v1/chat/completions", token, request); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	update([]ProviderCredential{{Provider: "openai", KeyName: "private", PoolID: "dedicated"}})
	sharedLease, err := server.scheduler.Acquire(context.Background(), "shared", "occupied-default", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if sharedLease != nil {
			sharedLease.Finish(0)
		}
	}()
	if w := call(server, "POST", "/v1/chat/completions", token, request); w.Code != 200 {
		t.Fatal("dedicated credential used blocked default pool", w.Body.String())
	}
	sharedLease.Finish(0)
	sharedLease = nil
	rows, err := store.Recent("storepilot", 10)
	if err != nil || rows[0].PriceSnapshot.KeyName != "private" || rows[0].PoolID != "dedicated" || rows[0].PriceSnapshot.AppliedPrice.CredentialSource != "project" || rows[0].Prediction.Samples != 0 {
		t.Fatal("credentials leaked into shared prediction/pool", rows, err)
	}
	estimate := `{"project_id":"storepilot","scene_id":"copy","request":` + request + `}`
	if w := call(server, "POST", "/api/llmhub/estimate", testAdmin, estimate); w.Code != 200 || !strings.Contains(w.Body.String(), `"samples":1`) {
		t.Fatal("estimate did not resolve project key", w.Code, w.Body.String())
	}
	update([]ProviderCredential{{Provider: "openai", KeyName: "broken", PoolID: "shared"}})
	if w := call(server, "POST", "/v1/chat/completions", token, request); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(keys) != 3 || keys[0] != "global" || keys[1] != "private" || keys[2] != "broken" {
		t.Fatal("project failure used default", keys)
	}
	update(nil)
	if w := call(server, "POST", "/v1/chat/completions", token, request); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	rows, err = store.Recent("storepilot", 10)
	if err != nil || rows[0].PriceSnapshot.KeyName != "global" || rows[0].Prediction.Samples != 1 || rows[0].PriceSnapshot.AppliedPrice.CredentialSource != "default" {
		t.Fatal("removing override lost default/history", rows, err)
	}
}

func TestQueuedCredentialChangeStopsDispatch(t *testing.T) {
	var calls atomic.Int64
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) })
	hold, err := server.scheduler.Acquire(context.Background(), "shared", "hold", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if hold != nil {
			hold.Finish(0)
		}
	}()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- call(server, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"hello"}]}`)
	}()
	deadline := time.Now().Add(time.Second)
	for server.scheduler.Stats()[0].Queued == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if server.scheduler.Stats()[0].Queued == 0 {
		t.Fatal("request never queued")
	}
	c, v, _ := store.Config()
	c.DefaultCredentials = []ProviderCredential{{Provider: "openai", KeyName: "rotated", PoolID: "shared"}}
	raw, _ := json.Marshal(map[string]any{"config": c, "version": v})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(raw)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	hold.Finish(0)
	hold = nil
	select {
	case w := <-result:
		if w.Code != 409 || !strings.Contains(w.Body.String(), "credentials_changed") {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued credential change hung")
	}
	if calls.Load() != 0 {
		t.Fatal("stale credentials dispatched")
	}
	if rows, err := store.Recent("storepilot", 10); err != nil || len(rows) != 0 {
		t.Fatal("rejected queue change billed", rows, err)
	}
}

func TestPredictionEstimateLearningAndConservativeBudget(t *testing.T) {
	var calls atomic.Int64
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_read_tokens":50}}}`)
	})
	server.now = func() time.Time { return time.Now().Add(10 * time.Second) }
	c := server.snapshot()
	rate := 0.2
	c.Profiles[0].CachedInputUSDPerMillion = &rate
	b, _ := json.Marshal(map[string]any{"config": c, "version": 2})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	request := `{"model":"copy","messages":[{"role":"user","content":"same exact prompt"}]}`
	for i := 0; i < 4; i++ {
		if w := call(server, "POST", "/v1/chat/completions", token, request); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	rows, err := store.Recent("storepilot", 10)
	if err != nil || len(rows) != 4 {
		t.Fatal(err, rows)
	}
	last := rows[0]
	if last.Prediction == nil || last.Prediction.Samples != 3 || last.Prediction.CacheHitProbability == nil || *last.Prediction.CacheHitProbability != 0.8 || last.Prediction.ErrorUSD == nil || last.CostUSD != 0.0001 || last.Estimated {
		t.Fatalf("prediction/settlement missing: %+v", last)
	}
	var n int
	if err := store.Settle(last.ID, "success", 200, 100, 20, 100, 1, false, last.PriceSnapshot); err != nil {
		t.Fatal(err)
	}
	store.db.QueryRow("SELECT COUNT(*) FROM prediction_observations").Scan(&n)
	if n != 4 {
		t.Fatal("duplicate settlement learned twice", n)
	}
	estimate := `{"project_id":"storepilot","scene_id":"copy","request":` + request + `}`
	if w := call(server, "POST", "/api/llmhub/estimate", token, estimate); w.Code != 401 {
		t.Fatal("project key accessed admin predictor", w.Code)
	}
	w := call(server, "POST", "/api/llmhub/estimate", testAdmin, estimate)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"samples":4`) || !strings.Contains(w.Body.String(), `"cache_hit_probability":`) || calls.Load() != 4 {
		t.Fatal("estimate called model or lost samples", w.Code, w.Body.String(), calls.Load())
	}
	for _, invalid := range []string{
		`{"project_id":"storepilot","scene_id":"absent","request":{}}`,
		`{"project_id":"storepilot","scene_id":"copy","request":{"messages":[{"role":"user","content":"hello"}],"extra_headers":{"Authorization":"bypass"}}}`,
	} {
		if w := call(server, "POST", "/api/llmhub/estimate", testAdmin, invalid); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	before, _ := store.Usage("storepilot", time.Now().UTC().Format("2006-01"))
	c = server.snapshot()
	c.Projects[0].MonthlyBudgetUSD = before[0].CostUSD + last.Prediction.ReservationUSD - 0.000001
	b, _ = json.Marshal(map[string]any{"config": c, "version": 3})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(server, "POST", "/v1/chat/completions", token, request); w.Code != 402 || calls.Load() != 4 {
		t.Fatal("discount prediction bypassed reservation", w.Code, w.Body.String())
	}
}

func TestUnknownWritesAndIncompleteUsageKeepReservation(t *testing.T) {
	for _, raw := range []string{
		`{"usage":{"prompt_tokens":100}}`,
		`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}}`,
	} {
		for _, stream := range []bool{false, true} {
			server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: "+raw+"\n\ndata: [DONE]\n\n")
				} else {
					io.WriteString(w, raw)
				}
			})
			body := `{"model":"copy","messages":[{"role":"user","content":"hello"}],"stream":false}`
			if stream {
				body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
			}
			if w := call(server, "POST", "/v1/chat/completions", token, body); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			rows, err := store.Recent("storepilot", 1)
			if err != nil || len(rows) != 1 || !rows[0].Estimated || rows[0].CostUSD != rows[0].Prediction.ReservationUSD || rows[0].Prediction.ErrorUSD != nil {
				t.Fatal("unknown tariff/partial usage treated as exact", rows, err)
			}
		}
	}
}

func TestDispatchPriceSnapshotSurvivesStreamBoundary(t *testing.T) {
	var clock atomic.Int64
	clock.Store(instant("2026-09-28T03:59:59Z").UnixNano())
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		clock.Store(instant("2026-09-28T04:00:01Z").UnixNano())
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_cache_hit_tokens\":50}}\n\ndata: [DONE]\n\n")
	})
	server.now = func() time.Time { return time.Unix(0, clock.Load()) }
	c := server.snapshot()
	c.Profiles[0] = tariffProfile()
	b, _ := json.Marshal(map[string]any{"config": c, "version": 2})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := call(server, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if w.Code != 200 || w.Header().Get("X-LLMHub-Price-Window") != "morning" {
		t.Fatal(w.Code, w.Body.String())
	}
	recent, err := store.Recent("storepilot", 10)
	if err != nil || len(recent) != 1 {
		t.Fatal(err)
	}
	a := recent[0]
	if a.Estimated || a.CostUSD != 0.00019 || a.PriceSnapshot.AppliedPrice.WindowID != "morning" || a.PriceSnapshot.AppliedPrice.CachedInputTokens != 50 {
		t.Fatalf("snapshot changed across stream boundary: %+v", a)
	}
	config, _, _ := store.Config()
	if config.Profiles[0].AppliedPrice != nil || config.Profiles[0].Pricing == nil {
		t.Fatal("persistent config was mutated")
	}
	if w := call(server, "GET", "/api/llmhub/pricing", testAdmin, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Off peak") {
		t.Fatal(w.Body.String())
	}
}

func TestQueuedAttemptRepricesAndReroutesBeforeDispatch(t *testing.T) {
	for _, policy := range []string{"ordered", "lowest_cost"} {
		t.Run(policy, func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(instant("2026-09-28T00:59:59Z").UnixNano())
			server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":0}}`)
			})
			server.now = func() time.Time { return time.Unix(0, clock.Load()) }
			c := server.snapshot()
			c.Profiles[0] = tariffProfile()
			p := c.Profiles[0]
			p.ID, p.Pricing, p.PoolID = "alternate", nil, "alternate"
			p.InputUSDPerMillion, p.OutputUSDPerMillion = 1.5, 3
			c.Profiles = append(c.Profiles, p)
			c.Pools = append(c.Pools, Pool{ID: "alternate", Concurrency: 1, QueueSize: 4})
			c.Scenes[0].Profiles, c.Scenes[0].RoutingPolicy = []string{"text-fast", "alternate"}, policy
			b, _ := json.Marshal(map[string]any{"config": c, "version": 2})
			if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			hold, err := server.scheduler.Acquire(context.Background(), "shared", "hold", 1)
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Finish(0)
			completed := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				completed <- call(server, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"hello"}]}`)
			}()
			deadline := time.Now().Add(time.Second)
			queued := false
			for time.Now().Before(deadline) {
				for _, stat := range server.scheduler.Stats() {
					if stat.ID == "shared" && stat.Queued == 1 {
						queued = true
					}
				}
				if queued {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !queued {
				t.Fatal("request did not enter queue")
			}
			clock.Store(instant("2026-09-28T01:00:00Z").UnixNano())
			hold.Finish(0)
			select {
			case w := <-completed:
				wantID, wantWindow, wantCost := "text-fast", "morning", 0.00028
				if policy == "lowest_cost" {
					wantID, wantWindow, wantCost = "alternate", "default", 0.00021
				}
				if w.Code != 200 || w.Header().Get("X-LLMHub-Profile") != wantID || w.Header().Get("X-LLMHub-Price-Window") != wantWindow {
					t.Fatal(w.Code, w.Header(), w.Body.String())
				}
				rows, _ := store.Recent("", 10)
				if len(rows) != 1 || rows[0].CostUSD != wantCost || rows[0].PriceSnapshot.AppliedPrice.PricedAt != instant("2026-09-28T01:00:00Z") {
					t.Fatalf("wrong admission snapshot: %+v", rows)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("queued request never completed")
			}
		})
	}
}

func TestMissingCacheUsageIsConservativeAndPeakBudgetApplies(t *testing.T) {
	var calls atomic.Int32
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	})
	server.now = func() time.Time { return instant("2026-09-28T02:00:00Z") }
	c := server.snapshot()
	c.Profiles[0] = tariffProfile()
	b, _ := json.Marshal(map[string]any{"config": c, "version": 2})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	body := `{"model":"copy","messages":[{"role":"user","content":"hello"}]}`
	if w := call(server, "POST", "/v1/chat/completions", token, body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	rows, _ := store.Recent("", 1)
	if !rows[0].Estimated || rows[0].CostUSD != 0.00028 || rows[0].PriceSnapshot.AppliedPrice.CacheUsageKnown {
		t.Fatalf("cache miss upper estimate required: %+v", rows[0])
	}
	c.Projects[0].MonthlyBudgetUSD = 0.000281
	b, _ = json.Marshal(map[string]any{"config": c, "version": 3})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(server, "POST", "/v1/chat/completions", token, body); w.Code != 402 || calls.Load() != 1 {
		t.Fatal("peak reservation bypassed budget")
	}
	if w := call(server, "GET", "/api/llmhub/pricing", token, ""); w.Code != 401 {
		t.Fatal("project key accessed admin pricing")
	}
}

func TestCostReroutingCannotResetRetryLimits(t *testing.T) {
	var clock atomic.Int64
	var primaryCalls, alternateCalls atomic.Int32
	clock.Store(instant("2026-09-28T00:59:59Z").UnixNano())
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BF-API-Key") == "alternate" {
			alternateCalls.Add(1)
			clock.Store(instant("2026-09-28T00:59:59Z").UnixNano())
		} else {
			primaryCalls.Add(1)
			clock.Store(instant("2026-09-28T02:00:00Z").UnixNano())
		}
		w.WriteHeader(503)
		io.WriteString(w, `{}`)
	})
	server.now = func() time.Time { return time.Unix(0, clock.Load()) }
	c := server.snapshot()
	c.Profiles[0] = tariffProfile()
	p := c.Profiles[0]
	p.ID, p.Pricing, p.PoolID, p.KeyName = "alternate", nil, "alternate", "alternate"
	p.InputUSDPerMillion, p.OutputUSDPerMillion = 1.5, 3
	c.Profiles = append(c.Profiles, p)
	c.Pools = append(c.Pools, Pool{ID: "alternate", Concurrency: 1, QueueSize: 4})
	c.Scenes[0].Profiles, c.Scenes[0].RoutingPolicy = []string{"text-fast", "alternate"}, "lowest_cost"
	c.Scenes[0].Retries, c.Scenes[0].TimeoutSeconds, c.Scenes[0].QueueTimeoutSeconds = 1, 10, 5
	b, _ := json.Marshal(map[string]any{"config": c, "version": 2})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := call(server, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 503 || primaryCalls.Load() != 2 || alternateCalls.Load() != 2 {
		t.Fatalf("price changes reset attempt limits: HTTP %d, primary=%d alternate=%d", w.Code, primaryCalls.Load(), alternateCalls.Load())
	}
	rows, _ := store.Recent("", 10)
	if len(rows) != 4 {
		t.Fatal("missing retry records")
	}
	for _, stat := range server.scheduler.Stats() {
		if stat.Active != 0 {
			t.Fatal("rerouting leaked lease")
		}
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testServer(t *testing.T, handler http.HandlerFunc) (*Server, *Store, string) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	store := testStore(t)
	if _, err := store.SaveConfig(testConfig(), 1); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store, upstream.URL, testAdmin, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	_, token, err := store.CreateKey("storepilot")
	if err != nil {
		t.Fatal(err)
	}
	return server, store, token
}

func call(server *Server, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

func TestOpenAICompatibilityAndAccounting(t *testing.T) {
	var path string
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-BF-API-Key") != "primary" {
			t.Error("project credentials leaked or selected provider key missing")
		}
		var b map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		if string(b["model"]) != `"openai/test-model"` {
			t.Error("model alias was not resolved")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chat-test","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	})
	w := call(server, "POST", "/v1/chat/completions", token, `{"model":"scene/copy","messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 200 {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	if path != "/openai/v1/chat/completions" {
		t.Fatalf("wrong Bifrost endpoint: %s", path)
	}
	attempts, err := store.Recent("storepilot", 10)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts: %v %v", attempts, err)
	}
	a := attempts[0]
	if a.Estimated || a.InputTokens != 10 || a.OutputTokens != 5 || a.CostUSD != 0.00002 || a.Status != "success" {
		t.Fatalf("wrong accounting: %+v", a)
	}
	if strings.Contains(w.Body.String(), "llh_") {
		t.Fatal("key leaked")
	}
}

func TestProjectIsolationBudgetAndRevocation(t *testing.T) {
	var calls atomic.Int32
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) })
	for _, tc := range []struct {
		token, body string
		status      int
	}{
		{"wrong", `{"model":"copy"}`, 401},
		{token, `{"model":"other-project","messages":[]}`, 403},
		{token, `{"model":"copy","messages":[{"role":"user","content":"hi"}],"extra_body":{"model":"expensive"}}`, 400},
	} {
		if w := call(server, "POST", "/v1/chat/completions", tc.token, tc.body); w.Code != tc.status {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
	}
	c := server.snapshot()
	c.Projects[0].MonthlyBudgetUSD = 0.000001
	b, _ := json.Marshal(map[string]any{"config": c, "version": 2})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	body := `{"model":"copy","messages":[{"role":"user","content":"hi"}]}`
	if w := call(server, "POST", "/v1/chat/completions", token, body); w.Code != 402 {
		t.Fatalf("budget: %d %s", w.Code, w.Body.String())
	}
	keys, _ := store.Keys()
	store.RevokeKey(keys[0].ID)
	if w := call(server, "POST", "/v1/chat/completions", token, body); w.Code != 401 {
		t.Fatal("revoked key accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid requests reached provider")
	}
}

func TestStreamUsageAndInterruptedStream(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "interrupted"}[terminal], func(t *testing.T) {
			server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
				if terminal {
					io.WriteString(w, "data: [DONE]\n\n")
				}
			})
			w := call(server, "POST", "/v1/chat/completions", token, `{"model":"copy","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("delta")) {
				t.Fatal(w.Body.String())
			}
			rows, _ := store.Recent("", 1)
			if rows[0].Estimated == terminal {
				t.Fatalf("incorrect stream reconciliation: %+v", rows[0])
			}
			if !terminal && rows[0].Status != "stream_error" {
				t.Fatal("truncated SSE marked successful")
			}
			if server.scheduler.Stats()[0].Active != 0 {
				t.Fatal("stream concurrency leaked")
			}
		})
	}
}

func TestFallbackAndConfigurationConflict(t *testing.T) {
	var calls atomic.Int32
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			io.WriteString(w, `{}`)
			return
		}
		io.WriteString(w, `{"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	})
	c := server.snapshot()
	p := c.Profiles[0]
	p.ID = "backup"
	p.PoolID = "backup"
	c.Profiles = append(c.Profiles, p)
	c.Pools = append(c.Pools, Pool{ID: "backup", Concurrency: 1, QueueSize: 2})
	c.Scenes[0].Profiles = []string{"text-fast", "backup"}
	b, _ := json.Marshal(map[string]any{"config": c, "version": 2})
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(server, "PUT", "/api/llmhub/config", testAdmin, string(b)); w.Code != 409 {
		t.Fatal("stale configuration overwritten")
	}
	w := call(server, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 || w.Header().Get("X-LLMHub-Profile") != "backup" {
		t.Fatalf("fallback failed: %d %s", w.Code, w.Body.String())
	}
	rows, _ := store.Recent("", 10)
	if len(rows) != 2 || calls.Load() != 2 {
		t.Fatal("fallback attempts missing")
	}
}
