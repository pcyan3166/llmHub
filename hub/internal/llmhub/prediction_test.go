package llmhub

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPredictionEvidenceIsolationExpiryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prediction.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	p := testConfig().Profiles[0]
	rate := 0.1
	p.CachedInputUSDPerMillion = &rate
	request, input, output, _, err := prepareRequest([]byte(`{"messages":[{"role":"user","content":"private customer text"}]}`), "/v1/chat/completions", p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	predict := func(project string, profile Profile, at time.Time, body []byte) *CostPrediction {
		t.Helper()
		v, err := s.predict(project, "copy", "/v1/chat/completions", "http://local", profile, body, input, output, at)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := predict("p", p, now, request)
	if first.CostUSD != first.ReservationUSD || first.CacheHitProbability != nil || first.Samples != 0 {
		t.Fatal(first)
	}
	for i := 0; i < 10; i++ {
		cache := int64(80)
		if i%2 == 0 {
			cache = 0
		}
		if _, err := s.db.Exec("INSERT INTO prediction_observations VALUES(?,?,?,?,?,?,?,?)", randomID("sample"), first.scope, first.fingerprint, now.Add(-time.Duration(10+i)*time.Second).UnixMilli(), 100, 20, cache, true); err != nil {
			t.Fatal(err)
		}
	}
	learned := predict("p", p, now, request)
	if learned.Samples != 10 || learned.CacheSamples != 10 || learned.Confidence != "medium" || learned.CacheHitProbability == nil || *learned.CacheHitProbability != 0.5 || learned.ExpectedCachedTokens != 40 || learned.CostUSD != 0.000104 || learned.ReservationUSD != first.ReservationUSD {
		t.Fatalf("unexpected learning: %+v", learned)
	}
	noHistory := []*CostPrediction{predict("other", p, now, request)}
	for _, field := range []string{"key", "model", "pool"} {
		changed := p
		switch field {
		case "key":
			changed.KeyName = "other"
		case "model":
			changed.Model = "other"
		case "pool":
			changed.PoolID = "other"
		}
		noHistory = append(noHistory, predict("p", changed, now, request))
	}
	changed := strings.Replace(string(request), "customer", "different", 1)
	noHistory = append(noHistory, predict("p", p, now, []byte(changed)))
	for _, v := range noHistory {
		if v.Samples != 0 || v.CacheHitProbability != nil {
			t.Fatal("scope leaked", v)
		}
	}
	stream := map[string]json.RawMessage{}
	json.Unmarshal(request, &stream)
	stream["stream"] = json.RawMessage(`true`)
	stream["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	streamBody, _ := json.Marshal(stream)
	if v := predict("p", p, now, streamBody); v.Samples != 10 {
		t.Fatal("unary/stream evidence split", v)
	}
	expired := predict("p", p, now.Add(6*time.Minute), request)
	if expired.Samples != 10 || expired.CacheHitProbability != nil || expired.ExpectedCachedTokens != 0 || expired.CostUSD != expired.UncachedUSD {
		t.Fatal("stale cache prediction", expired)
	}
	peak := p
	peak.InputUSDPerMillion = 2
	peak.OutputUSDPerMillion = 4
	if v := predict("p", peak, now, request); v.CostUSD != 0.000204 {
		t.Fatal("current time tariff not used", v)
	}
	if _, err := s.db.Exec("INSERT INTO prediction_observations VALUES(?,?,?,?,?,?,?,?)", "unknown", first.scope, first.fingerprint, now.Add(-time.Second).UnixMilli(), 100, 20, 0, false); err != nil {
		t.Fatal(err)
	}
	if v := predict("p", p, now, request); v.CacheHitProbability != nil || v.CostUSD != v.UncachedUSD {
		t.Fatal("missing latest usage discounted", v)
	}
	raw, _ := json.Marshal(learned)
	if strings.Contains(string(raw), "customer") || strings.Contains(string(raw), first.fingerprint) || strings.Contains(string(raw), first.scope) {
		t.Fatal("private fingerprint leaked")
	}
	secret := string(s.predictionKey)
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(s.predictionKey) != secret || predict("p", p, now, request).Samples != 11 {
		t.Fatal("history not persisted")
	}
}

func TestPredictionDoesNotLearnFailuresOrInflateConfidence(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := testConfig().Profiles[0].priceAt(time.Now())
	body := []byte(`{"messages":[{"role":"user","content":"x"}]}`)
	pred, err := s.predict("p", "copy", "/v1/chat/completions", "local", p, body, 1000, 32, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		out := int64(1)
		if i%2 == 0 {
			out = 32
		}
		_, err = s.db.Exec("INSERT INTO prediction_observations VALUES(?,?,?,?,?,?,?,?)", randomID("sample"), pred.scope, pred.fingerprint, time.Now().Add(-10*time.Second).UnixMilli(), 100, out, 0, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	v, err := s.predict("p", "copy", "/v1/chat/completions", "local", p, body, 1000, 32, time.Now())
	if err != nil || v.Confidence != "low" || math.IsNaN(v.CostUSD) {
		t.Fatal(v, err)
	}
	for _, status := range []string{"error", "stream_error", "retry", "interrupted"} {
		a := Attempt{ID: randomID("attempt"), ProjectID: "p", SceneID: "copy", PriceSnapshot: &p, Prediction: pred}
		if err := s.Reserve(a, 1, 100); err != nil {
			t.Fatal(err)
		}
		p.AppliedPrice.UsageKnown = true
		if err := s.Settle(a.ID, status, 500, 100, 1, 100, 1, false, &p); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM prediction_observations").Scan(&n); err != nil || n != 32 {
		t.Fatal("failure learned", n, err)
	}
}
