package main

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pcyan3166/llmHub/hub/internal/llmhub"
)

func TestDemoRestartPreservesConfigurationAndKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.db")
	store, err := llmhub.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c, fresh, err := seedDemo(store, "../../../deploy/llmhub/seed.json")
	if err != nil || !fresh {
		t.Fatal(err)
	}
	c.Profiles[0].InputUSDPerMillion = 7
	if _, err = store.SaveConfig(c, 2); err != nil {
		t.Fatal(err)
	}
	_, token, err := store.CreateKey(c.Projects[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = llmhub.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, fresh, err = seedDemo(store, "nonexistent-seed")
	if err != nil || fresh || c.Profiles[0].InputUSDPerMillion != 7 {
		t.Fatal("restart overwrote demo configuration", err)
	}
	if _, err = store.Authenticate(token); err != nil {
		t.Fatal("restart invalidated project key", err)
	}
}

func TestDemoPlatformKeyRegistrationIsRedacted(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/providers/openai/keys", strings.NewReader(`{"name":"local-reference","value":"sk-do-not-return"}`))
	mockProvider(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "sk-do-not-return") || !strings.Contains(w.Body.String(), `"name":"local-reference"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mockProvider(w, httptest.NewRequest("DELETE", "/api/providers/openai/keys/local-reference", nil))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
