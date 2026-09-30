package llmhub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// A byte based upper estimate is deliberately conservative for text payloads.
// Stateful and media inputs cannot be bounded this way and are rejected.
func prepareRequest(raw []byte, endpoint string, p Profile) ([]byte, int64, int64, bool, error) {
	if endpoint == "/v1/images/generations" {
		return prepareImageRequest(raw, p)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, 0, 0, false, fmt.Errorf("request must be a JSON object")
	}
	allowed := map[string]bool{"model": true, "messages": true, "input": true, "stream": true, "stream_options": true, "n": true, "max_tokens": true, "max_completion_tokens": true, "max_output_tokens": true, "temperature": true, "top_p": true, "frequency_penalty": true, "presence_penalty": true, "stop": true, "seed": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true, "response_format": true, "text": true, "reasoning": true, "reasoning_effort": true, "instructions": true, "metadata": true, "user": true, "store": true, "encoding_format": true, "dimensions": true, "truncation": true}
	for key := range body {
		if !allowed[key] {
			return nil, 0, 0, false, fmt.Errorf("unsupported request field %q", key)
		}
	}
	for _, key := range []string{"fallbacks", "provider", "extra_headers", "extra_body", "api_key", "previous_response_id", "conversation", "prompt", "bifrost"} {
		if _, ok := body[key]; ok {
			return nil, 0, 0, false, fmt.Errorf("field %q is managed by llmHub or unsupported", key)
		}
	}
	if err := textOnly(body, endpoint); err != nil {
		return nil, 0, 0, false, err
	}
	var stream bool
	if v, ok := body["stream"]; ok {
		if err := json.Unmarshal(v, &stream); err != nil {
			return nil, 0, 0, false, fmt.Errorf("stream must be a boolean")
		}
	}
	if endpoint == "/v1/embeddings" && stream {
		return nil, 0, 0, false, fmt.Errorf("embeddings do not support streaming")
	}
	if v, ok := body["n"]; ok {
		var n int
		if json.Unmarshal(v, &n) != nil || n != 1 {
			return nil, 0, 0, false, fmt.Errorf("only n=1 is supported")
		}
	}
	var output int64
	if endpoint != "/v1/embeddings" {
		output = p.MaxOutputTokens
		found := 0
		for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
			if v, ok := body[key]; ok {
				found++
				var n float64
				if json.Unmarshal(v, &n) != nil || math.Trunc(n) != n || n < 1 || n > float64(p.MaxOutputTokens) {
					return nil, 0, 0, false, fmt.Errorf("%s must be an integer between 1 and %d", key, p.MaxOutputTokens)
				}
				output = int64(n)
			}
		}
		if found > 1 {
			return nil, 0, 0, false, fmt.Errorf("use only one output token limit")
		}
		if endpoint == "/v1/responses" {
			delete(body, "max_tokens")
			delete(body, "max_completion_tokens")
			body["max_output_tokens"] = json.RawMessage(fmt.Sprint(output))
		} else {
			delete(body, "max_tokens")
			delete(body, "max_output_tokens")
			body["max_completion_tokens"] = json.RawMessage(fmt.Sprint(output))
		}
	}
	body["model"], _ = json.Marshal(p.Provider + "/" + p.Model)
	if stream && endpoint == "/v1/chat/completions" {
		body["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	if endpoint == "/v1/responses" {
		body["store"] = json.RawMessage(`false`)
	}
	out, err := json.Marshal(body)
	// Request bytes include text, tools, roles and a fixed protocol overhead allowance.
	input := int64(len(out))*2 + 1024
	return out, input, output, stream, err
}

func prepareImageRequest(raw []byte, p Profile) ([]byte, int64, int64, bool, error) {
	var b struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		N              *int   `json:"n"`
		Size           string `json:"size"`
		Quality        string `json:"quality"`
		ResponseFormat string `json:"response_format"`
		User           string `json:"user,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return nil, 0, 0, false, fmt.Errorf("invalid image request: %w", err)
	}
	if strings.TrimSpace(b.Prompt) == "" || b.N != nil && *b.N != 1 || b.Size != "" && b.Size != p.ImageSize || b.Quality != "" && b.Quality != p.ImageQuality || b.ResponseFormat != "" && b.ResponseFormat != "b64_json" && b.ResponseFormat != "url" {
		return nil, 0, 0, false, fmt.Errorf("image request requires a prompt, n=1 and the profile's configured size and quality")
	}
	one := 1
	b.Model, b.N, b.Size, b.Quality = p.Provider+"/"+p.Model, &one, p.ImageSize, p.ImageQuality
	if b.ResponseFormat == "" {
		b.ResponseFormat = "b64_json"
	}
	out, err := json.Marshal(b)
	return out, int64(len(out))*2 + 1024, 0, false, err
}

func textOnly(body map[string]json.RawMessage, endpoint string) error {
	var value any
	key := "messages"
	if endpoint == "/v1/responses" || endpoint == "/v1/embeddings" {
		key = "input"
	}
	if v, ok := body[key]; !ok || json.Unmarshal(v, &value) != nil || value == nil {
		return fmt.Errorf("%s is required", key)
	}
	if endpoint == "/v1/embeddings" {
		switch v := value.(type) {
		case string:
			if v == "" {
				return fmt.Errorf("input is empty")
			}
		case []any:
			if len(v) == 0 {
				return fmt.Errorf("input is empty")
			}
			for _, item := range v {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("embedding input must be text strings")
				}
			}
		default:
			return fmt.Errorf("embedding input must be text")
		}
	}
	if endpoint == "/v1/chat/completions" {
		if messages, ok := value.([]any); !ok || len(messages) == 0 {
			return fmt.Errorf("messages must be a nonempty array")
		}
	}
	var walk func(any) error
	walk = func(v any) error {
		switch item := v.(type) {
		case map[string]any:
			if typ, ok := item["type"].(string); ok && (strings.Contains(typ, "image") || strings.Contains(typ, "audio") || strings.Contains(typ, "file") || strings.Contains(typ, "video") || strings.Contains(typ, "search") || strings.Contains(typ, "computer")) {
				return fmt.Errorf("media and server-side tools require separate accounting and are not supported")
			}
			for key, child := range item {
				if key == "image_url" || key == "input_audio" || key == "file_id" || key == "audio" {
					return fmt.Errorf("only stateless text requests are supported")
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range item {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, key := range []string{"messages", "input", "tools", "modalities", "audio"} {
		if raw, ok := body[key]; ok {
			if key == "audio" {
				return fmt.Errorf("audio output is unsupported")
			}
			var v any
			if json.Unmarshal(raw, &v) != nil {
				return fmt.Errorf("invalid %s", key)
			}
			if key == "modalities" && !bytes.Equal(bytes.TrimSpace(raw), []byte(`["text"]`)) {
				return fmt.Errorf("only text output is supported")
			}
			if err := walk(v); err != nil {
				return err
			}
		}
	}
	return nil
}

type tokenUsage struct {
	input  int64
	output int64
	known  bool
}

func parseUsage(raw []byte) tokenUsage {
	var payload struct {
		Usage *struct {
			Prompt     *int64 `json:"prompt_tokens"`
			Completion *int64 `json:"completion_tokens"`
			Input      *int64 `json:"input_tokens"`
			Output     *int64 `json:"output_tokens"`
			Total      *int64 `json:"total_tokens"`
		} `json:"usage"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return tokenUsage{}
	}
	if len(payload.Response) > 0 {
		if u := parseUsage(payload.Response); u.known {
			return u
		}
	}
	u := payload.Usage
	if u == nil {
		return tokenUsage{}
	}
	result := tokenUsage{}
	if u.Prompt != nil {
		result.input = *u.Prompt
		result.known = true
	} else if u.Input != nil {
		result.input = *u.Input
		result.known = true
	}
	if u.Completion != nil {
		result.output = *u.Completion
	} else if u.Output != nil {
		result.output = *u.Output
	}
	if result.input < 0 || result.output < 0 || result.input > 1000000000 || result.output > 1000000000 {
		return tokenUsage{}
	}
	return result
}

// SSE tracking buffers at most one line; it never accumulates the response.
type usageTracker struct {
	line     []byte
	overflow bool
	usage    tokenUsage
	failed   bool
	terminal bool
}

func (t *usageTracker) Write(chunk []byte) {
	for _, b := range chunk {
		if b == '\n' {
			t.consume()
			t.line = t.line[:0]
			t.overflow = false
			continue
		}
		if len(t.line) < 1024*1024 && !t.overflow {
			t.line = append(t.line, b)
		} else {
			t.line = t.line[:0]
			t.overflow = true
		}
	}
}

func (t *usageTracker) consume() {
	if t.overflow {
		return
	}
	line := bytes.TrimSpace(t.line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	raw := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(raw, []byte("[DONE]")) {
		t.terminal = true
		return
	}
	if u := parseUsage(raw); u.known {
		t.usage = u
	}
	var event struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &event) == nil {
		if event.Type == "response.completed" || event.Type == "response.incomplete" {
			t.terminal = true
		}
		if event.Type == "response.failed" || len(event.Error) > 0 && string(event.Error) != "null" {
			t.failed = true
		}
	}
}
