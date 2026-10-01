package llmhub

import (
	"encoding/json"
	"testing"
)

func TestRequestValidationAndAlias(t *testing.T) {
	p := testConfig().Profiles[0]
	for _, raw := range []string{
		`{"messages":[]}`, `{"messages":[{"role":"user","content":"hi"}],"n":2}`,
		`{"messages":[{"role":"user","content":"hi"}],"max_tokens":33}`,
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image"}}]}]}`,
		`{"messages":[{"role":"user","content":"hi"}],"previous_response_id":"stateful"}`,
	} {
		if _, _, _, _, err := prepareRequest([]byte(raw), "/v1/chat/completions", p); err == nil {
			t.Fatalf("accepted invalid payload: %s", raw)
		}
	}
	body, input, output, stream, err := prepareRequest([]byte(`{"input":"hello","max_output_tokens":10,"stream":true}`), "/v1/responses", p)
	if err != nil || input < 1000 || output != 10 || !stream {
		t.Fatalf("bad reservation: %s %v", body, err)
	}
	var b map[string]json.RawMessage
	json.Unmarshal(body, &b)
	if string(b["store"]) != "false" || string(b["model"]) != `"openai/test-model"` {
		t.Fatal(string(body))
	}
}

func TestOfficialOpenAIPricesPinStandardServiceTier(t *testing.T) {
	p := testConfig().Profiles[0]
	p.FollowOfficial = true
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses"} {
		raw := `{"messages":[{"role":"user","content":"hello"}],"input":"hello"}`
		body, _, _, _, err := prepareRequest([]byte(raw), endpoint, p)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]json.RawMessage
		json.Unmarshal(body, &value)
		if string(value["service_tier"]) != `"default"` {
			t.Fatal("account default could opt into premium pricing", string(body))
		}
		if _, _, _, _, err := prepareRequest([]byte(`{"messages":[{"role":"user","content":"hello"}],"input":"hello","service_tier":"priority"}`), endpoint, p); err == nil {
			t.Fatal("caller overrode standard tariff")
		}
	}
	p.FollowOfficial = false
	body, _, _, _, _ := prepareRequest([]byte(`{"messages":[{"role":"user","content":"hello"}]}`), "/v1/chat/completions", p)
	var value map[string]json.RawMessage
	json.Unmarshal(body, &value)
	if _, ok := value["service_tier"]; ok {
		t.Fatal("manual profile behavior changed")
	}
}

func TestImageTariffAndImmutableSize(t *testing.T) {
	p := Profile{Provider: "openai", Model: "image-test", ImageUSDPerImage: 0.05, ImageSize: "1024x1024", ImageQuality: "standard"}
	body, _, output, stream, err := prepareRequest([]byte(`{"model":"scene/image.generate","prompt":"test"}`), "/v1/images/generations", p)
	if err != nil || output != 0 || stream || costMicros(p, 0, 0) != 50000 {
		t.Fatalf("image: %s %v", body, err)
	}
	for _, raw := range []string{`{"prompt":"test","n":2}`, `{"prompt":"test","size":"1024x1536"}`, `{"prompt":"test","quality":"hd"}`, `{"prompt":"test","extra_body":{}}`} {
		if _, _, _, _, err := prepareRequest([]byte(raw), "/v1/images/generations", p); err == nil {
			t.Fatal("image accounting bypass")
		}
	}
}

func TestUsageTrackerAcrossFragments(t *testing.T) {
	data := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":3}}}\n\n")
	for step := 1; step < 20; step++ {
		tracker := usageTracker{}
		for i := 0; i < len(data); i += step {
			tracker.Write(data[i:min(i+step, len(data))])
		}
		if !tracker.terminal || !tracker.usage.known || tracker.usage.input != 8 || tracker.usage.output != 3 {
			t.Fatalf("step %d: %+v", step, tracker)
		}
	}
	if parseUsage([]byte(`{"usage":{"prompt_tokens":-1,"completion_tokens":1}}`)).known {
		t.Fatal("negative usage accepted")
	}
}
