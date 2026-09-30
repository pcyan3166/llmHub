package llmhub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type CatalogSettings struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"`
}

type CatalogQuote struct {
	Name           string            `json:"name"`
	Model          string            `json:"model,omitempty"`
	Input          float64           `json:"input"`
	Output         float64           `json:"output"`
	Cached         *float64          `json:"cached,omitempty"`
	Peak           *CatalogQuote     `json:"peak,omitempty"`
	Automatic      bool              `json:"automatic"`
	Conditions     string            `json:"conditions"`
	BillingDetails map[string]string `json:"billing_details,omitempty"`
}

type CatalogSource struct {
	Provider      string         `json:"provider"`
	URL           string         `json:"url"`
	CheckedAt     time.Time      `json:"checked_at"`
	VerifiedAt    time.Time      `json:"verified_at"`
	Hash          string         `json:"hash"`
	TermsHash     string         `json:"terms_hash"`
	Error         string         `json:"error,omitempty"`
	Failures      int            `json:"failures"`
	Quotes        []CatalogQuote `json:"quotes"`
	NewsURL       string         `json:"news_url"`
	NewsCheckedAt time.Time      `json:"news_checked_at"`
	NewsHash      string         `json:"news_hash"`
	NewsError     string         `json:"news_error,omitempty"`
	NewsModels    []string       `json:"news_models"`
}

type CatalogEvent struct {
	At       time.Time     `json:"at"`
	Provider string        `json:"provider"`
	Model    string        `json:"model,omitempty"`
	Kind     string        `json:"kind"`
	Before   *CatalogQuote `json:"before,omitempty"`
	After    *CatalogQuote `json:"after,omitempty"`
	Profiles []string      `json:"profiles,omitempty"`
	Hash     string        `json:"hash"`
}

type CatalogState struct {
	Sources []CatalogSource `json:"sources"`
	Events  []CatalogEvent  `json:"events"`
}

type CatalogProfile struct {
	ID         string        `json:"id"`
	Status     string        `json:"status"`
	Reason     string        `json:"reason"`
	SourceURL  string        `json:"source_url,omitempty"`
	VerifiedAt time.Time     `json:"verified_at"`
	CanApply   bool          `json:"can_apply"`
	Quote      *CatalogQuote `json:"quote,omitempty"`
	Hash       string        `json:"hash,omitempty"`
}

var catalogHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var officialSources = []CatalogSource{
	{Provider: "deepseek", URL: "https://api-docs.deepseek.com/quick_start/pricing/", NewsURL: "https://api-docs.deepseek.com/updates/"},
	{Provider: "openai", URL: "https://developers.openai.com/api/docs/pricing.md", NewsURL: "https://developers.openai.com/api/docs/changelog.md"},
	{Provider: "anthropic", URL: "https://platform.claude.com/docs/en/about-claude/pricing.md", NewsURL: "https://platform.claude.com/docs/en/models/overview.md"},
}

func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func (c Config) catalogSettings() CatalogSettings {
	if c.Catalog != nil {
		return *c.Catalog
	}
	return CatalogSettings{Enabled: true, IntervalMinutes: 360}
}

func (s *Store) catalogState() (CatalogState, error) {
	state := CatalogState{Sources: append([]CatalogSource(nil), officialSources...), Events: []CatalogEvent{}}
	var raw string
	err := s.db.QueryRow("SELECT state FROM catalog_state WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal([]byte(raw), &state)
	for i := range state.Sources {
		for _, source := range officialSources {
			if state.Sources[i].Provider == source.Provider {
				state.Sources[i].URL = source.URL
				state.Sources[i].NewsURL = source.NewsURL
			}
		}
	}
	return state, err
}

type catalogWriter interface {
	Exec(string, ...any) (sql.Result, error)
}

func writeCatalog(db catalogWriter, state CatalogState) error {
	if len(state.Events) > 1000 {
		state.Events = state.Events[len(state.Events)-1000:]
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO catalog_state VALUES(1,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state", string(raw))
	return err
}

type Catalog struct {
	server                   *Server
	client                   *http.Client
	fetch                    func(context.Context, string) ([]byte, error)
	ctx                      context.Context
	cancel                   context.CancelFunc
	mu                       sync.Mutex
	wg                       sync.WaitGroup
	running, closed, started bool
	lastStart                time.Time
}

func newCatalog(s *Server) *Catalog {
	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second, MaxConnsPerHost: 2}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c := &Catalog{server: s, ctx: ctx, cancel: cancel, client: client}
	c.fetch = c.fetchOfficial
	return c
}

func (c *Catalog) fetchOfficial(ctx context.Context, address string) ([]byte, error) {
	allowed := false
	for _, s := range officialSources {
		if address == s.URL || address == s.NewsURL {
			allowed = true
		}
	}
	if !allowed {
		return nil, fmt.Errorf("source is not an approved official URL")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "llmHub-price-monitor/1.0")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("official source returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err == nil && len(raw) > 2<<20 {
		err = fmt.Errorf("official page exceeds 2 MiB")
	}
	return raw, err
}

// StartCatalog starts one bounded worker; NewServer alone never makes external calls.
func (s *Server) StartCatalog(parent context.Context) {
	c := s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.closed {
		return
	}
	c.started = true
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		c.kick(false)
		for {
			select {
			case <-parent.Done():
				c.cancel()
				return
			case <-c.ctx.Done():
				return
			case <-tick.C:
				c.kick(false)
			}
		}
	}()
}

func (c *Catalog) close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
	c.client.CloseIdleConnections()
}

func (c *Catalog) kick(force bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.running || force && c.server.now().Sub(c.lastStart) < time.Minute {
		return false
	}
	c.running = true
	if force {
		c.lastStart = c.server.now()
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() { c.mu.Lock(); c.running = false; c.mu.Unlock() }()
		if err := c.check(force); err != nil && c.ctx.Err() == nil {
			c.server.logger.Error("official catalog check failed", "error", err)
		}
	}()
	return true
}

func sourceDue(source CatalogSource, settings CatalogSettings) time.Time {
	delay := time.Duration(settings.IntervalMinutes) * time.Minute
	if source.Failures > 0 {
		n := min(source.Failures-1, 6)
		delay = min(delay, 5*time.Minute*time.Duration(1<<n))
	}
	return source.CheckedAt.Add(delay)
}

func (c *Catalog) check(force bool) error {
	s := c.server
	settings := s.snapshot().catalogSettings()
	if !force && !settings.Enabled {
		return nil
	}
	state, err := s.store.catalogState()
	if err != nil {
		return err
	}
	changed := false
	for i, old := range state.Sources {
		if !force && s.now().Before(sourceDue(old, settings)) {
			continue
		}
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
		raw, fetchErr := c.fetch(ctx, old.URL)
		cancel()
		at := s.now().UTC()
		source := old
		source.CheckedAt = at
		var quotes []CatalogQuote
		var terms string
		if fetchErr == nil {
			quotes, terms, fetchErr = parseCatalog(old.Provider, raw)
		}
		if fetchErr != nil {
			source.Failures++
			source.Error = fetchErr.Error()
			if source.Error != old.Error {
				state.Events = append(state.Events, CatalogEvent{At: at, Provider: old.Provider, Kind: "check_failed", Hash: old.Hash})
			}
		} else {
			source.Hash = digest(raw)
			source.TermsHash = terms
			source.Error = ""
			source.Failures = 0
			source.VerifiedAt = at
			source.Quotes = quotes
			state.Events = append(state.Events, catalogChanges(old, source, at)...)
		}
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		newsCtx, newsCancel := context.WithTimeout(c.ctx, 20*time.Second)
		newsRaw, newsErr := c.fetch(newsCtx, source.NewsURL)
		newsCancel()
		var newsModels []string
		var newsHash string
		if newsErr == nil {
			newsModels, newsHash, newsErr = parseCatalogNews(source.Provider, newsRaw)
		}
		source.NewsCheckedAt = s.now().UTC()
		if newsErr != nil {
			source.NewsError = newsErr.Error()
			if source.NewsError != old.NewsError {
				state.Events = append(state.Events, CatalogEvent{At: at, Provider: source.Provider, Kind: "news_check_failed", Hash: source.NewsHash})
			}
		} else {
			source.NewsError = ""
			source.NewsHash = newsHash
			source.NewsModels = newsModels
			if old.NewsHash != "" && old.NewsHash != newsHash {
				state.Events = append(state.Events, CatalogEvent{At: at, Provider: source.Provider, Kind: "news_changed", Hash: newsHash})
			}
			seen := map[string]bool{}
			for _, id := range old.NewsModels {
				seen[id] = true
			}
			for _, id := range newsModels {
				if !seen[id] {
					state.Events = append(state.Events, CatalogEvent{At: at, Provider: source.Provider, Model: id, Kind: "model_mentioned", Hash: newsHash})
				}
			}
		}
		state.Sources[i] = source
		changed = true
	}
	if !changed {
		return nil
	}
	// Re-read the live configuration after fetching, never overwrite an admin's concurrent edits.
	s.configMu.Lock()
	defer s.configMu.Unlock()
	config := s.config
	config.Profiles = append([]Profile(nil), config.Profiles...)
	updated := false
	for i, p := range config.Profiles {
		if !p.FollowOfficial {
			continue
		}
		source, q := catalogQuote(state, p)
		if source == nil || q == nil || source.Error != "" || source.VerifiedAt.IsZero() || s.now().Sub(source.VerifiedAt) > 2*time.Duration(config.catalogSettings().IntervalMinutes)*time.Minute || p.OfficialTerms != source.TermsHash {
			continue
		}
		next, err := officialTariff(p, *q, s.now(), false)
		if err != nil || sameTariff(p, next) {
			continue
		}
		config.Profiles[i] = next
		updated = true
		state.Events = append(state.Events, CatalogEvent{At: s.now().UTC(), Provider: p.Provider, Model: p.Model, Kind: "applied", Profiles: []string{p.ID}, Hash: source.Hash, After: q})
	}
	if updated {
		version, err := s.store.saveConfigAndCatalog(config, s.version, &state)
		if err != nil {
			return err
		}
		s.config = config
		s.version = version
		return nil
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	return writeCatalog(s.store.db, state)
}

func catalogChanges(old, next CatalogSource, at time.Time) []CatalogEvent {
	events := []CatalogEvent{}
	if old.Hash != "" && old.Hash == next.Hash && old.TermsHash == next.TermsHash {
		return events
	}
	if old.Hash != "" && old.TermsHash != next.TermsHash {
		events = append(events, CatalogEvent{At: at, Provider: next.Provider, Kind: "terms_changed", Hash: next.Hash})
	}
	before := map[string]CatalogQuote{}
	for _, q := range old.Quotes {
		before[q.Name] = q
	}
	for _, q := range next.Quotes {
		prev, found := before[q.Name]
		delete(before, q.Name)
		kind := "new_model"
		if found {
			a, _ := json.Marshal(prev)
			b, _ := json.Marshal(q)
			if string(a) == string(b) {
				continue
			}
			kind = "price_changed"
		}
		e := CatalogEvent{At: at, Provider: next.Provider, Model: q.Name, Kind: kind, Hash: next.Hash, After: &q}
		if found {
			e.Before = &prev
		}
		events = append(events, e)
	}
	for name, q := range before {
		events = append(events, CatalogEvent{At: at, Provider: next.Provider, Model: name, Kind: "not_listed", Hash: next.Hash, Before: &q})
	}
	return events
}

func catalogQuote(state CatalogState, p Profile) (*CatalogSource, *CatalogQuote) {
	for i := range state.Sources {
		source := &state.Sources[i]
		if source.Provider != p.Provider {
			continue
		}
		for j := range source.Quotes {
			q := &source.Quotes[j]
			if q.Model != "" && q.Model == p.Model {
				return source, q
			}
		}
		return source, nil
	}
	return nil, nil
}

func sameTariff(a, b Profile) bool {
	a.FollowOfficial = false
	b.FollowOfficial = false
	a.OfficialTerms = ""
	b.OfficialTerms = ""
	a.OfficialCalendarYear = 0
	b.OfficialCalendarYear = 0
	a.OfficialCalendarHash = ""
	b.OfficialCalendarHash = ""
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func officialTariff(p Profile, q CatalogQuote, at time.Time, approve bool) (Profile, error) {
	if !q.Automatic || p.ImageUSDPerImage > 0 {
		return p, fmt.Errorf("报价含未支持的计费条件，需人工核对")
	}
	if q.Peak != nil {
		if p.Pricing == nil || p.Pricing.Timezone != "UTC" || p.Pricing.CalendarTimezone != "Asia/Shanghai" || len(p.Pricing.Windows) != 2 || len(p.Pricing.ExcludedDates) == 0 {
			return p, fmt.Errorf("请先配置官方峰谷时段与完整中国节假日日历")
		}
		if !approve && (p.OfficialCalendarYear != at.In(shanghaiLocation()).Year() || p.OfficialCalendarHash != calendarHash(p)) {
			return p, fmt.Errorf("新年度节假日日历待确认")
		}
		schedule := *p.Pricing
		schedule.Windows = append([]PriceWindow(nil), schedule.Windows...)
		seen := map[string]bool{}
		for i, w := range schedule.Windows {
			key := w.Start + "-" + w.End
			if key != "01:00-04:00" && key != "06:00-10:00" || seen[key] || len(w.Days) != 5 {
				return p, fmt.Errorf("峰谷时段与官方规则不一致")
			}
			seen[key] = true
			days := map[int]bool{}
			for _, d := range w.Days {
				days[d] = true
			}
			for d := 1; d <= 5; d++ {
				if !days[d] {
					return p, fmt.Errorf("峰谷星期与官方规则不一致")
				}
			}
			schedule.Windows[i].InputUSDPerMillion = q.Peak.Input
			schedule.Windows[i].OutputUSDPerMillion = q.Peak.Output
			schedule.Windows[i].CachedInputUSDPerMillion = q.Peak.Cached
		}
		year := at.In(shanghaiLocation()).Year()
		covered := false
		for _, date := range schedule.ExcludedDates {
			if strings.HasPrefix(date, fmt.Sprintf("%d-", year)) {
				covered = true
			}
		}
		if !covered {
			return p, fmt.Errorf("当前年度节假日日历缺失")
		}
		p.Pricing = &schedule
		p.OfficialCalendarYear = year
		p.OfficialCalendarHash = calendarHash(p)
	} else if p.Pricing != nil {
		return p, fmt.Errorf("官方固定价不能覆盖自定义时段，请先核对并移除时段")
	}
	p.InputUSDPerMillion = q.Input
	p.OutputUSDPerMillion = q.Output
	p.CachedInputUSDPerMillion = q.Cached
	return p, nil
}

func shanghaiLocation() *time.Location { loc, _ := time.LoadLocation("Asia/Shanghai"); return loc }

func calendarHash(p Profile) string {
	if p.Pricing == nil {
		return ""
	}
	dates := append([]string(nil), p.Pricing.ExcludedDates...)
	sort.Strings(dates)
	raw, _ := json.Marshal(dates)
	return digest(append([]byte(p.Pricing.CalendarTimezone+"/"), raw...))
}

func (c *Catalog) verifiedProfile(p Profile) (*CatalogSource, error) {
	if !p.FollowOfficial {
		return nil, nil
	}
	state, err := c.server.store.catalogState()
	if err != nil {
		return nil, err
	}
	v := c.profileStatus(state, p)
	if v.Status != "verified" {
		return nil, fmt.Errorf("profile %s: %s", p.ID, v.Reason)
	}
	source, _ := catalogQuote(state, p)
	return source, nil
}

func (c *Catalog) profileStatus(state CatalogState, p Profile) CatalogProfile {
	v := CatalogProfile{ID: p.ID, Status: "manual", Reason: "手动价格"}
	source, q := catalogQuote(state, p)
	if source == nil {
		v.Reason = "该供应商暂未支持官方检查"
		return v
	}
	v.SourceURL = source.URL
	v.VerifiedAt = source.VerifiedAt
	v.Hash = source.Hash
	v.Quote = q
	if source.Error != "" || source.VerifiedAt.IsZero() || c.server.now().Sub(source.VerifiedAt) > 2*time.Duration(c.server.snapshot().catalogSettings().IntervalMinutes)*time.Minute {
		v.Status = "stale"
		v.Reason = "官方报价未验证或已过期"
		return v
	}
	if q == nil {
		v.Status = "review"
		v.Reason = "未找到精确模型 ID，不能推测别名价格"
		return v
	}
	next, err := officialTariff(p, *q, c.server.now(), true)
	if err != nil {
		v.Status = "review"
		v.Reason = err.Error()
		return v
	}
	v.CanApply = true
	if p.FollowOfficial && p.OfficialTerms == source.TermsHash && sameTariff(p, next) && (q.Peak == nil || p.OfficialCalendarYear == c.server.now().In(shanghaiLocation()).Year() && p.OfficialCalendarHash == calendarHash(p)) {
		v.Status = "verified"
		v.Reason = "跟随官方报价"
	} else {
		v.Status = "review"
		v.Reason = "待管理员采纳并确认计费条件"
	}
	return v
}

func (c *Catalog) admin(w http.ResponseWriter, r *http.Request) {
	s := c.server
	if r.URL.Path == "/api/llmhub/catalog/check" && r.Method == "POST" {
		if !c.kick(true) {
			writeError(w, 429, "catalog_busy", "检查正在进行，或距离上次检查不足一分钟")
			return
		}
		writeJSON(w, 202, map[string]bool{"running": true})
		return
	}
	state, err := s.store.catalogState()
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.URL.Path == "/api/llmhub/catalog" && r.Method == "GET" {
		profiles := []CatalogProfile{}
		config := s.snapshot()
		for _, p := range config.Profiles {
			profiles = append(profiles, c.profileStatus(state, p))
		}
		c.mu.Lock()
		running := c.running
		c.mu.Unlock()
		due := map[string]time.Time{}
		for _, source := range state.Sources {
			due[source.Provider] = sourceDue(source, config.catalogSettings())
		}
		writeJSON(w, 200, map[string]any{"settings": config.catalogSettings(), "sources": state.Sources, "events": state.Events, "profiles": profiles, "running": running, "next_checks": due, "at": s.now().UTC()})
		return
	}
	if r.URL.Path == "/api/llmhub/catalog/apply" && r.Method == "POST" {
		var body struct {
			ProfileID         string `json:"profile_id"`
			Hash              string `json:"hash"`
			Version           int64  `json:"version"`
			ConfirmConditions bool   `json:"confirm_conditions"`
		}
		if err := readJSON(w, r, &body); err != nil || !body.ConfirmConditions {
			writeError(w, 400, "invalid_request", "需要确认官方计费条件和节假日日历")
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.running || c.closed {
			writeError(w, 409, "catalog_busy", "请等待本次检查完成")
			return
		}
		// Reload under the worker lock so a stale proposal can never be approved.
		state, err = s.store.catalogState()
		if err != nil {
			s.fail(w, err)
			return
		}
		s.configMu.Lock()
		defer s.configMu.Unlock()
		if body.Version != s.version {
			writeError(w, 409, "version_conflict", "配置已更新，请刷新后重试")
			return
		}
		config := s.config
		config.Profiles = append([]Profile(nil), config.Profiles...)
		for i, p := range config.Profiles {
			if p.ID != body.ProfileID {
				continue
			}
			source, q := catalogQuote(state, p)
			if source == nil || q == nil || source.Hash != body.Hash || source.Error != "" || s.now().Sub(source.VerifiedAt) > 2*time.Duration(config.catalogSettings().IntervalMinutes)*time.Minute {
				writeError(w, 409, "stale_quote", "官方报价已过期或变化，请重新检查")
				return
			}
			next, err := officialTariff(p, *q, s.now(), true)
			if err != nil {
				writeError(w, 400, "review_required", err.Error())
				return
			}
			next.FollowOfficial = true
			next.OfficialTerms = source.TermsHash
			config.Profiles[i] = next
			state.Events = append(state.Events, CatalogEvent{At: s.now().UTC(), Provider: p.Provider, Model: p.Model, Kind: "approved", Profiles: []string{p.ID}, Hash: source.Hash, After: q})
			version, err := s.store.saveConfigAndCatalog(config, s.version, &state)
			if err != nil {
				s.fail(w, err)
				return
			}
			s.config = config
			s.version = version
			writeJSON(w, 200, map[string]any{"config": config, "version": version})
			return
		}
		writeError(w, 404, "not_found", "profile not found")
		return
	}
	writeError(w, 404, "not_found", "catalog route not found")
}
