package llmhub

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestDeepSeekPricingExample(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/llmhub/deepseek-pricing.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var p Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if err := p.validatePricing(); err != nil {
		t.Fatal(err)
	}
	peak := p.priceAt(instant("2026-09-28T02:00:00Z"))
	holiday := p.priceAt(instant("2026-10-01T02:00:00Z"))
	if peak.InputUSDPerMillion != 0.3 || *peak.CachedInputUSDPerMillion != 0.006 || holiday.InputUSDPerMillion != 0.15 || holiday.AppliedPrice.WindowID != "excluded_date" {
		t.Fatal("example does not match configured peak/holiday rates")
	}
}

func tariffProfile() Profile {
	p := testConfig().Profiles[0]
	cached := 0.2
	p.CachedInputUSDPerMillion = &cached
	p.Pricing = &PriceSchedule{Timezone: "UTC", CalendarTimezone: "Asia/Shanghai", DefaultLabel: "Off peak", ExcludedDates: []string{"2026-10-01"}, Windows: []PriceWindow{{ID: "morning", Label: "Peak", Days: []int{1, 2, 3, 4, 5}, Start: "01:00", End: "04:00", InputUSDPerMillion: 2, OutputUSDPerMillion: 4, CachedInputUSDPerMillion: &cached}, {ID: "afternoon", Label: "Peak", Days: []int{1, 2, 3, 4, 5}, Start: "06:00", End: "10:00", InputUSDPerMillion: 2, OutputUSDPerMillion: 4, CachedInputUSDPerMillion: &cached}}}
	return p
}

func instant(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPricingWindowBoundariesAndCalendar(t *testing.T) {
	p := tariffProfile()
	if err := p.validatePricing(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ at, window string }{{"2026-09-28T00:59:59Z", "default"}, {"2026-09-28T01:00:00Z", "morning"}, {"2026-09-28T03:59:59Z", "morning"}, {"2026-09-28T04:00:00Z", "default"}, {"2026-09-28T06:00:00Z", "afternoon"}, {"2026-09-28T10:00:00Z", "default"}, {"2026-10-03T02:00:00Z", "default"}, {"2026-10-01T02:00:00Z", "excluded_date"}} {
		t.Run(tc.at, func(t *testing.T) {
			got := p.priceAt(instant(tc.at))
			if got.AppliedPrice.WindowID != tc.window {
				t.Fatalf("got %s, want %s", got.AppliedPrice.WindowID, tc.window)
			}
			if got.Pricing != nil || !got.AppliedPrice.PricedAt.Equal(instant(tc.at)) {
				t.Fatal("snapshot not resolved")
			}
		})
	}
	if p.AppliedPrice != nil || p.InputUSDPerMillion != 1 || len(p.Pricing.Windows) != 2 {
		t.Fatal("configuration was mutated")
	}
	p.Pricing.Windows = []PriceWindow{{ID: "night", Label: "Night", Days: []int{1}, Start: "23:00", End: "02:00", InputUSDPerMillion: 2, OutputUSDPerMillion: 4}}
	for _, at := range []string{"2026-09-28T23:00:00Z", "2026-09-29T01:59:59Z"} {
		if p.priceAt(instant(at)).AppliedPrice.WindowID != "night" {
			t.Fatal(at)
		}
	}
	if p.priceAt(instant("2026-09-29T02:00:00Z")).AppliedPrice.WindowID != "default" {
		t.Fatal("end must be exclusive")
	}
	p.Pricing.ExcludedDates = []string{"2026-09-29"}
	if p.priceAt(instant("2026-09-28T23:00:00Z")).AppliedPrice.WindowID != "excluded_date" {
		t.Fatal("calendar must use Asia/Shanghai, not UTC")
	}
}

func TestPricingRejectsInvalidAndOverlappingSchedules(t *testing.T) {
	for _, mutate := range []func(*Profile){func(p *Profile) { p.Pricing.Timezone = "Local" }, func(p *Profile) { p.Pricing.Timezone = "Invalid/Zone" }, func(p *Profile) { p.Pricing.Windows[0].End = "01:00" }, func(p *Profile) { p.Pricing.Windows[0].Start = "25:00" }, func(p *Profile) { p.Pricing.Windows[1].Start = "03:00" }, func(p *Profile) { p.Pricing.Windows[0].Days = []int{0} }, func(p *Profile) { p.Pricing.ExcludedDates = []string{"2026-02-30"} }, func(p *Profile) { p.AppliedPrice = &AppliedPrice{} }, func(p *Profile) { v := 3.0; p.CachedInputUSDPerMillion = &v }} {
		p := tariffProfile()
		mutate(&p)
		if p.validatePricing() == nil {
			t.Fatalf("accepted invalid profile: %+v", p)
		}
	}
	p := tariffProfile()
	p.Pricing.Windows = []PriceWindow{{ID: "night", Label: "Night", Days: []int{7}, Start: "23:00", End: "02:00"}, {ID: "monday", Label: "Monday", Days: []int{1}, Start: "01:00", End: "03:00"}}
	if p.validatePricing() == nil {
		t.Fatal("cross-week overlap accepted")
	}
}

func TestPricingUsesLocalWallClockAcrossDST(t *testing.T) {
	p := tariffProfile()
	p.Pricing.Timezone = "America/New_York"
	p.Pricing.ExcludedDates = nil
	p.Pricing.Windows = []PriceWindow{{ID: "night", Label: "Night", Days: []int{7}, Start: "01:00", End: "03:00", InputUSDPerMillion: 2, OutputUSDPerMillion: 4}}
	// Both occurrences of 01:30 in the autumn fold match the local tariff.
	for _, at := range []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z", "2026-03-08T06:30:00Z"} {
		if p.priceAt(instant(at)).AppliedPrice.WindowID != "night" {
			t.Fatal(at)
		}
	}
	if p.priceAt(instant("2026-03-08T07:00:00Z")).AppliedPrice.WindowID != "default" {
		t.Fatal("spring jump to 03:00 must end the window")
	}
}

func TestCostRoutingChangesWithTimeAndPreservesTies(t *testing.T) {
	c := testConfig()
	c.Profiles[0] = tariffProfile()
	alternate := c.Profiles[0]
	alternate.ID = "alternate"
	alternate.Pricing = nil
	alternate.InputUSDPerMillion = 1.5
	alternate.OutputUSDPerMillion = 3
	c.Profiles = append(c.Profiles, alternate)
	s := c.Scenes[0]
	s.Profiles = append(s.Profiles, "alternate")
	s.RoutingPolicy = "lowest_cost"
	raw := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	if cheapestRoute(c, s, s.Profiles, raw, instant("2026-09-28T00:00:00Z")) != "text-fast" {
		t.Fatal("off-peak route")
	}
	if cheapestRoute(c, s, s.Profiles, raw, instant("2026-09-28T02:00:00Z")) != "alternate" {
		t.Fatal("peak route")
	}
	s.RoutingPolicy = "ordered"
	if cheapestRoute(c, s, s.Profiles, raw, instant("2026-09-28T02:00:00Z")) != "text-fast" {
		t.Fatal("ordered route changed")
	}
	s.RoutingPolicy = "lowest_cost"
	c.Profiles[1].InputUSDPerMillion = 1
	c.Profiles[1].OutputUSDPerMillion = 2
	if cheapestRoute(c, s, s.Profiles, raw, instant("2026-09-28T00:00:00Z")) != "text-fast" {
		t.Fatal("tie changed configured order")
	}
}

func TestCacheUsageAndCost(t *testing.T) {
	p := tariffProfile().priceAt(instant("2026-09-28T00:00:00Z"))
	for _, raw := range []string{`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":50}}`, `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":50}}}`, `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_read_tokens":50}}}`, `{"response":{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":50}}}}`} {
		u := parseUsage([]byte(raw))
		if !u.cachedKnown || u.cached != 50 || usageCostMicros(p, u) != 100 {
			t.Fatalf("wrong cached usage: %+v", u)
		}
	}
	u := parseUsage([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":101}}`))
	if u.cachedKnown || usageCostMicros(p, u) != 140 {
		t.Fatal("invalid cached usage must use conservative miss price")
	}
}

func TestCacheWritesAreVisibleButNeverReadDiscounts(t *testing.T) {
	p := tariffProfile().priceAt(instant("2026-09-28T00:00:00Z"))
	for _, raw := range []string{
		`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_read_tokens":20,"cached_write_tokens":30}}}`,
		`{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}}`,
	} {
		u := parseUsage([]byte(raw))
		if !u.known || !u.cachedKnown || !u.cacheWriteKnown || u.cached != 20 || u.cacheWrite != 30 || !usageEstimated(p, u) {
			t.Fatal("cache writes must remain estimated without a supported write tariff", u)
		}
	}
	for _, raw := range []string{
		`{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":81}}}`,
		`{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cache_write_tokens":-1}}}`,
	} {
		if u := parseUsage([]byte(raw)); u.known {
			t.Fatal("invalid writes accepted", u)
		}
	}
}
