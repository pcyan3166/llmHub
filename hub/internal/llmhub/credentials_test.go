package llmhub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPlatformKeySaveIsWriteOnlyAndUsesBifrost(t *testing.T) {
	const secret = "sk-local-fixture-only-do-not-use"
	registered := ""
	posts := 0
	server, store, token := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/providers/") {
			user, password, ok := r.BasicAuth()
			if !ok || user != "admin-fixture" || password != "password-fixture" {
				t.Error("management credentials missing")
			}
			var key struct {
				Name, Value string
				Models      []string
				Weight      int
			}
			json.NewDecoder(r.Body).Decode(&key)
			if key.Value != secret || key.Weight != 1 || len(key.Models) != 1 || key.Models[0] != "*" {
				t.Error("platform key payload", key.Name)
			}
			registered = key.Name
			posts++
			json.NewEncoder(w).Encode(map[string]string{"id": "managed-key-id", "name": registered})
			return
		}
		user, password, ok := r.BasicAuth()
		if r.Header.Get("X-BF-API-Key") != registered || !ok || user != "admin-fixture" || password != "password-fixture" {
			t.Error("dispatch did not use private reference")
		}
		io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	server.bifrostUser, server.bifrostPassword = "admin-fixture", "password-fixture"
	c, v, _ := store.Config()
	c.Projects[0].Credentials = []ProviderCredential{{Provider: "openai", APIKey: secret}}
	raw, _ := json.Marshal(map[string]any{"config": c, "version": v})
	saved := call(server, "PUT", "/api/llmhub/config", testAdmin, string(raw))
	if saved.Code != 200 || posts != 1 || !strings.HasPrefix(registered, "llmhub-") {
		t.Fatal(saved.Code, saved.Body.String(), posts)
	}
	if strings.Contains(saved.Body.String(), secret) || strings.Contains(saved.Body.String(), `"api_key"`) {
		t.Fatal("secret returned")
	}
	for _, path := range []string{"/api/llmhub/config", "/api/llmhub/overview"} {
		if res := call(server, "GET", path, testAdmin, ""); strings.Contains(res.Body.String(), secret) {
			t.Fatal("secret in read API")
		}
	}
	var leaked int
	if err := store.db.QueryRow("SELECT count(*) FROM config_history WHERE instr(config,?)>0", secret).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("plaintext in configuration history", err)
	}
	profile, source := server.snapshot().resolvedProfile("storepilot", "text-fast")
	if profile.PoolID != "shared" || source != "project" {
		t.Fatal("optional pool did not inherit", profile, source)
	}
	if res := call(server, "POST", "/v1/chat/completions", token, `{"model":"copy","messages":[{"role":"user","content":"test"}]}`); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	// Blank means keep the existing key; configuration reads never refill plaintext.
	c, v, _ = store.Config()
	raw, _ = json.Marshal(map[string]any{"config": c, "version": v})
	if res := call(server, "PUT", "/api/llmhub/config", testAdmin, string(raw)); res.Code != 200 || posts != 1 {
		t.Fatal("blank replaced existing key", res.Code, posts)
	}
}

func TestPlatformKeyRegistrationDoesNotBlockReadsOrOverwriteConcurrentEdits(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	deletes := 0
	server, store, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes++
			w.WriteHeader(204)
			return
		}
		var key struct{ Name string }
		json.NewDecoder(r.Body).Decode(&key)
		close(entered)
		<-release
		json.NewEncoder(w).Encode(map[string]string{"id": "created-key-id", "name": key.Name})
	})
	c, v, _ := store.Config()
	c.DefaultCredentials = []ProviderCredential{{Provider: "openai", APIKey: "sk-local-concurrent-fixture"}}
	raw, _ := json.Marshal(map[string]any{"config": c, "version": v})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(server, "PUT", "/api/llmhub/config", testAdmin, string(raw)) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("registration did not start")
	}
	read := make(chan *httptest.ResponseRecorder, 1)
	go func() { read <- call(server, "GET", "/api/llmhub/config", testAdmin, "") }()
	select {
	case res := <-read:
		if res.Code != 200 {
			t.Fatal(res.Code)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("registration blocked config reads")
	}
	other, v, _ := store.Config()
	other.Projects[0].Name = "Concurrent edit"
	update, _ := json.Marshal(map[string]any{"config": other, "version": v})
	res := call(server, "PUT", "/api/llmhub/config", testAdmin, string(update))
	close(release)
	if res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	select {
	case res = <-done:
		if res.Code != 409 || deletes != 1 {
			t.Fatal("conflicting save not rolled back", res.Code, deletes)
		}
	case <-time.After(time.Second):
		t.Fatal("registration hung")
	}
	current, _, _ := store.Config()
	if current.Projects[0].Name != "Concurrent edit" || len(current.DefaultCredentials) != 0 {
		t.Fatal("concurrent edit overwritten")
	}
}

func TestPlatformKeySaveFailureRollbackValidationAndConflict(t *testing.T) {
	const secret = "sk-local-redaction-fixture"
	posts, deletes := 0, 0
	server, store, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes++
			w.WriteHeader(204)
			return
		}
		posts++
		var key struct{ Name string }
		json.NewDecoder(r.Body).Decode(&key)
		if posts == 2 {
			w.WriteHeader(401)
			io.WriteString(w, secret)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "created-key-id", "name": key.Name})
	})
	original, v, _ := store.Config()
	c := server.snapshot()
	c.DefaultCredentials = []ProviderCredential{{Provider: "openai", APIKey: secret}, {Provider: "deepseek", APIKey: secret}}
	raw, _ := json.Marshal(map[string]any{"config": c, "version": v - 1})
	if res := call(server, "PUT", "/api/llmhub/config", testAdmin, string(raw)); res.Code != 409 || posts != 0 {
		t.Fatal("stale version registered key")
	}
	c.DefaultCredentials[0].PoolID = "absent"
	raw, _ = json.Marshal(map[string]any{"config": c, "version": v})
	if res := call(server, "PUT", "/api/llmhub/config", testAdmin, string(raw)); res.Code != 400 || posts != 0 || !strings.Contains(res.Body.String(), "限额池") {
		t.Fatal("invalid pool registered key", res.Body.String())
	}
	c.DefaultCredentials[0].PoolID = ""
	raw, _ = json.Marshal(map[string]any{"config": c, "version": v})
	res := call(server, "PUT", "/api/llmhub/config", testAdmin, string(raw))
	if res.Code != 502 || deletes != 1 || strings.Contains(res.Body.String(), secret) || !strings.Contains(res.Body.String(), "授权失败") {
		t.Fatal("failure leaked secret or skipped rollback", res.Code, deletes, res.Body.String())
	}
	current, version, _ := store.Config()
	old, _ := json.Marshal(original)
	now, _ := json.Marshal(current)
	if version != v || string(old) != string(now) {
		t.Fatal("partial save changed configuration")
	}
	if c.Validate() == nil {
		t.Fatal("store accepts write-only plaintext")
	}
}
