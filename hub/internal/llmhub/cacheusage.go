package llmhub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// The private native Bifrost route can carry DeepSeek's original usage even on
// releases that discard cache fields in the OpenAI compatibility converter.
// Only validated usage is merged; raw requests/responses never reach the client.
func normalizeCacheUsage(raw []byte) []byte {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return raw
	}
	var extras struct {
		RawResponse json.RawMessage `json:"raw_response"`
	}
	json.Unmarshal(body["extra_fields"], &extras)
	delete(body, "extra_fields")
	native := extras.RawResponse
	var text string
	if json.Unmarshal(native, &text) == nil {
		native = []byte(text)
	}
	original, normalized := parseUsage(native), parseUsage(raw)
	if original.known && original.cachedKnown && normalized.known && original.input == normalized.input && original.output == normalized.output {
		var usage map[string]json.RawMessage
		if json.Unmarshal(body["usage"], &usage) == nil && usage != nil {
			usage["prompt_cache_hit_tokens"], _ = json.Marshal(original.cached)
			usage["prompt_cache_miss_tokens"], _ = json.Marshal(original.input - original.cached)
			body["usage"], _ = json.Marshal(usage)
		}
	}
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}

// At most one SSE line is buffered, with a hard 1 MiB limit. Bifrost emits one
// JSON payload per data line on this native route.
type cacheUsageReader struct {
	reader  *bufio.Reader
	pending []byte
	err     error
}

func newCacheUsageReader(r io.Reader) *cacheUsageReader {
	return &cacheUsageReader{reader: bufio.NewReaderSize(r, 16<<10)}
}

func (r *cacheUsageReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 && r.err == nil {
		var line []byte
		for {
			part, err := r.reader.ReadSlice('\n')
			if len(line)+len(part) > 1<<20 {
				r.err = fmt.Errorf("native SSE line exceeds 1 MiB")
				return 0, r.err
			}
			line = append(line, part...)
			if err == bufio.ErrBufferFull {
				continue
			}
			r.err = err
			break
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			raw := bytes.TrimSpace(line[5:])
			if !bytes.Equal(raw, []byte("[DONE]")) && json.Valid(raw) {
				line = append([]byte("data: "), normalizeCacheUsage(raw)...)
				line = append(line, '\n')
			}
		}
		r.pending = line
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	if n > 0 {
		return n, nil
	}
	return 0, r.err
}
