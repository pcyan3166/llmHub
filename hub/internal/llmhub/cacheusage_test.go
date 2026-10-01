package llmhub

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestNativeCacheUsageNormalization(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		raw := json.RawMessage(`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":40}}`)
		var value any = raw
		if encoded {
			value = string(raw)
		}
		body, _ := json.Marshal(map[string]any{"choices": []any{}, "usage": json.RawMessage(`{"prompt_tokens":100,"completion_tokens":20}`), "extra_fields": map[string]any{"raw_response": value, "raw_request": "secret prompt", "key_id": "private-account"}})
		out := normalizeCacheUsage(body)
		u := parseUsage(out)
		if !u.cachedKnown || u.cached != 40 || strings.Contains(string(out), "extra_fields") || strings.Contains(string(out), "secret") {
			t.Fatalf("invalid bridge: %s", out)
		}
		sse := "data: " + string(body) + "\n\ndata: [DONE]\n\n"
		reader := newCacheUsageReader(strings.NewReader(sse))
		var output strings.Builder
		if _, err := io.CopyBuffer(&output, reader, make([]byte, 3)); err != nil {
			t.Fatal(err)
		}
		tracker := usageTracker{}
		tracker.Write([]byte(output.String()))
		if !tracker.terminal || tracker.usage.cached != 40 || strings.Contains(output.String(), "raw_response") {
			t.Fatal(output.String())
		}
	}
	for _, raw := range []string{
		`{"usage":{"prompt_tokens":100,"completion_tokens":20},"extra_fields":{"raw_response":"{\"usage\":{\"prompt_tokens\":101,\"completion_tokens\":20,\"prompt_cache_hit_tokens\":40}}"}}`,
		`{"usage":{"prompt_tokens":100,"completion_tokens":20},"extra_fields":{"raw_response":"{\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_cache_hit_tokens\":101}}"}}`,
		`{"usage":{"prompt_tokens":100,"completion_tokens":20}}`,
	} {
		if u := parseUsage(normalizeCacheUsage([]byte(raw))); !u.known || u.cachedKnown {
			t.Fatal("mismatching or absent raw usage became a hit", u)
		}
	}
}

func TestNativeCacheSSELimitAndEOF(t *testing.T) {
	if _, err := io.ReadAll(newCacheUsageReader(strings.NewReader("data: " + strings.Repeat("x", 1<<20)))); err == nil {
		t.Fatal("oversized line accepted")
	}
	for _, text := range []string{"", "data: [DONE]", "event: message\r\n\ndata: [DONE]\r\n\n"} {
		out, err := io.ReadAll(newCacheUsageReader(strings.NewReader(text)))
		if err != nil || string(out) != text {
			t.Fatalf("EOF handling: %q %v", out, err)
		}
	}
}
