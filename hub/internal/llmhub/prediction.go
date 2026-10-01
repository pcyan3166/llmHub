package llmhub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"time"
)

// Prediction is advisory. It never changes reservation, routing or settlement.
type CostPrediction struct {
	CostUSD              float64   `json:"cost_usd"`
	UncachedUSD          float64   `json:"uncached_usd"`
	ReservationUSD       float64   `json:"reservation_usd"`
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`
	ExpectedCachedTokens float64   `json:"expected_cached_tokens"`
	CacheHitProbability  *float64  `json:"cache_hit_probability"`
	Confidence           string    `json:"confidence"`
	CacheConfidence      string    `json:"cache_confidence"`
	Samples              int       `json:"samples"`
	CacheSamples         int       `json:"cache_samples"`
	Method               string    `json:"method"`
	At                   time.Time `json:"at"`
	CacheHorizonSeconds  int       `json:"cache_horizon_seconds"`
	ErrorUSD             *float64  `json:"error_usd,omitempty"`
	scope, fingerprint   string
}

func predictionConfidence(n int) string {
	if n >= 30 {
		return "high"
	}
	if n >= 10 {
		return "medium"
	}
	return "low"
}

func (s *Store) predictionFingerprint(v any) string {
	raw, _ := json.Marshal(v)
	h := hmac.New(sha256.New, s.predictionKey)
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// Exact request matching deliberately excludes partial/semantic prefixes. Five
// minutes is an evidence window, not a claim about a provider's actual cache TTL.
func (s *Store) predict(project, scene, endpoint, upstream string, p Profile, body []byte, input, output int64, at time.Time) (*CostPrediction, error) {
	result := &CostPrediction{InputTokens: input, OutputTokens: output, Confidence: "low", CacheConfidence: "unknown", Method: "conservative_no_history", At: at.UTC(), CacheHorizonSeconds: 300}
	result.ReservationUSD = float64(costMicros(p, input, output)) / 1e6
	result.scope = s.predictionFingerprint([]string{project, scene, endpoint, upstream, p.Provider, p.Model, p.KeyName, p.PoolID})
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	delete(request, "stream")
	delete(request, "stream_options")
	result.fingerprint = s.predictionFingerprint(request)
	rows, err := s.db.Query(`SELECT at_ms,input_tokens,output_tokens,cached_tokens,cache_known FROM prediction_observations WHERE scope=? AND fingerprint=? AND at_ms>=? AND at_ms<=? ORDER BY at_ms DESC,attempt_id DESC LIMIT 32`, result.scope, result.fingerprint, at.AddDate(0, 0, -30).UnixMilli(), at.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inputs, outputs, outputSquares, cached float64
	hits := 0
	latestCacheKnown := false
	for rows.Next() {
		var ms, in, out, cache int64
		var known bool
		if err := rows.Scan(&ms, &in, &out, &cache, &known); err != nil {
			return nil, err
		}
		if result.Samples == 0 {
			latestCacheKnown = known
		}
		result.Samples++
		inputs += float64(in)
		outputs += float64(out)
		outputSquares += float64(out) * float64(out)
		age := at.Sub(time.UnixMilli(ms))
		if known && age >= 5*time.Second && age <= 5*time.Minute {
			result.CacheSamples++
			cached += float64(cache)
			if cache > 0 {
				hits++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result.Samples > 0 {
		result.Method = "exact_request_history"
		result.InputTokens = min(input, int64(math.Ceil(inputs/float64(result.Samples))))
		result.OutputTokens = min(output, int64(math.Ceil(outputs/float64(result.Samples))))
		result.Confidence = predictionConfidence(result.Samples)
		mean := outputs / float64(result.Samples)
		if mean > 0 && math.Sqrt(max(0, outputSquares/float64(result.Samples)-mean*mean))/mean > 0.5 {
			result.Confidence = "low"
		}
	}
	if latestCacheKnown && result.CacheSamples >= 3 && p.CachedInputUSDPerMillion != nil && p.ImageUSDPerImage == 0 {
		// Beta(1,1) smoothing avoids reporting a guaranteed hit from few samples.
		probability := float64(hits+1) / float64(result.CacheSamples+2)
		result.CacheHitProbability = &probability
		result.CacheConfidence = predictionConfidence(result.CacheSamples)
		if hits > 0 {
			result.ExpectedCachedTokens = min(float64(result.InputTokens), cached/float64(hits)) * probability
		}
	}
	uncachedMicros := float64(result.InputTokens)*p.InputUSDPerMillion + float64(result.OutputTokens)*p.OutputUSDPerMillion + p.ImageUSDPerImage*1e6
	result.UncachedUSD = math.Ceil(uncachedMicros) / 1e6
	result.CostUSD = result.UncachedUSD
	if p.CachedInputUSDPerMillion != nil {
		result.CostUSD = math.Ceil(uncachedMicros-result.ExpectedCachedTokens*(p.InputUSDPerMillion-*p.CachedInputUSDPerMillion)) / 1e6
	}
	return result, nil
}

func (s *Server) estimate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string          `json:"project_id"`
		SceneID   string          `json:"scene_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	c := s.snapshot()
	p, ok := c.Project(body.ProjectID)
	scene, found := c.Scene(body.ProjectID, body.SceneID)
	if !ok || !p.Enabled || !found {
		writeError(w, 400, "invalid_scene", "an enabled project and its scene are required")
		return
	}
	at := s.now()
	type candidate struct {
		ProfileID  string          `json:"profile_id"`
		Prediction *CostPrediction `json:"prediction"`
		Error      string          `json:"error,omitempty"`
	}
	candidates := []candidate{}
	for _, id := range scene.Profiles {
		profile, _ := c.resolvedProfile(body.ProjectID, id)
		profile = profile.priceAt(at)
		if _, err := s.catalog.verifiedProfile(c.Profile(id)); err != nil {
			candidates = append(candidates, candidate{ProfileID: id, Error: err.Error()})
			continue
		}
		prepared, input, output, _, err := prepareRequest(body.Request, scene.Endpoint, profile)
		if err != nil {
			writeError(w, 400, "invalid_request", err.Error())
			return
		}
		prediction, err := s.store.predict(body.ProjectID, scene.ID, scene.Endpoint, s.upstream.String(), profile, prepared, input, output, at)
		if err != nil {
			s.fail(w, err)
			return
		}
		candidates = append(candidates, candidate{ProfileID: id, Prediction: prediction})
	}
	writeJSON(w, 200, map[string]any{"candidates": candidates, "at": at.UTC()})
}
