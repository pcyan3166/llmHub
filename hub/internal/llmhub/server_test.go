package llmhub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
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
