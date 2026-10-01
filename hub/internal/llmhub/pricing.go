package llmhub

import (
	"fmt"
	"math"
	"time"
	_ "time/tzdata"
)

type PriceSchedule struct {
	Timezone         string        `json:"timezone"`
	CalendarTimezone string        `json:"calendar_timezone,omitempty"`
	DefaultLabel     string        `json:"default_label"`
	ExcludedDates    []string      `json:"excluded_dates,omitempty"`
	Windows          []PriceWindow `json:"windows"`
}

type PriceWindow struct {
	ID                       string   `json:"id"`
	Label                    string   `json:"label"`
	Days                     []int    `json:"days"`
	Start                    string   `json:"start"`
	End                      string   `json:"end"`
	InputUSDPerMillion       float64  `json:"input_usd_per_million"`
	OutputUSDPerMillion      float64  `json:"output_usd_per_million"`
	CachedInputUSDPerMillion *float64 `json:"cached_input_usd_per_million,omitempty"`
	ImageUSDPerImage         float64  `json:"image_usd_per_image,omitempty"`
}

type AppliedPrice struct {
	Label                string     `json:"label"`
	WindowID             string     `json:"window_id"`
	Timezone             string     `json:"timezone"`
	PricedAt             time.Time  `json:"priced_at"`
	CachedInputTokens    int64      `json:"cached_input_tokens"`
	CacheUsageKnown      bool       `json:"cache_usage_known"`
	UsageKnown           bool       `json:"usage_known"`
	CacheWriteTokens     int64      `json:"cache_write_tokens"`
	CacheWriteUsageKnown bool       `json:"cache_write_usage_known"`
	OfficialSourceURL    string     `json:"official_source_url,omitempty"`
	OfficialSourceHash   string     `json:"official_source_hash,omitempty"`
	OfficialVerifiedAt   *time.Time `json:"official_verified_at,omitempty"`
}

func clockMinute(value string, end bool) (int, error) {
	if end && value == "24:00" {
		return 1440, nil
	}
	t, err := time.Parse("15:04", value)
	if err != nil || len(value) != 5 {
		return 0, fmt.Errorf("time must use HH:MM (end may use 24:00)")
	}
	return t.Hour()*60 + t.Minute(), nil
}

func cachedPriceValid(cached *float64, miss float64) bool {
	return cached == nil || validPrice(*cached) && *cached <= miss
}

func (p Profile) validatePricing() error {
	if p.AppliedPrice != nil {
		return fmt.Errorf("applied_price is read-only")
	}
	if !cachedPriceValid(p.CachedInputUSDPerMillion, p.InputUSDPerMillion) {
		return fmt.Errorf("cached input price must be between zero and input price")
	}
	s := p.Pricing
	if s == nil {
		return nil
	}
	if len(s.Windows) > 32 || len(s.ExcludedDates) > 730 || s.DefaultLabel == "" || len(s.DefaultLabel) > 64 {
		return fmt.Errorf("invalid pricing schedule limits or default label")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" || s.Timezone == "Local" {
		return fmt.Errorf("pricing requires an explicit IANA timezone")
	}
	if s.CalendarTimezone != "" {
		if _, err := time.LoadLocation(s.CalendarTimezone); err != nil || s.CalendarTimezone == "Local" {
			return fmt.Errorf("invalid calendar timezone")
		}
	}
	dates := map[string]bool{}
	for _, date := range s.ExcludedDates {
		if _, err := time.Parse("2006-01-02", date); err != nil || dates[date] {
			return fmt.Errorf("invalid or duplicate excluded date %q", date)
		}
		dates[date] = true
	}
	occupied := make([]bool, 7*1440)
	ids := map[string]bool{}
	for _, w := range s.Windows {
		start, e1 := clockMinute(w.Start, false)
		end, e2 := clockMinute(w.End, true)
		if !idPattern.MatchString(w.ID) || ids[w.ID] || w.Label == "" || len(w.Label) > 64 || e1 != nil || e2 != nil || start == end || len(w.Days) < 1 || len(w.Days) > 7 || !validPrice(w.InputUSDPerMillion) || !validPrice(w.OutputUSDPerMillion) || !cachedPriceValid(w.CachedInputUSDPerMillion, w.InputUSDPerMillion) || !validPrice(w.ImageUSDPerImage) || (p.ImageUSDPerImage > 0) != (w.ImageUSDPerImage > 0) {
			return fmt.Errorf("invalid price window %q", w.ID)
		}
		ids[w.ID] = true
		days := map[int]bool{}
		duration := end - start
		if duration < 0 {
			duration += 1440
		}
		for _, day := range w.Days {
			if day < 1 || day > 7 || days[day] {
				return fmt.Errorf("window days must be unique ISO weekdays 1..7")
			}
			days[day] = true
			for i := 0; i < duration; i++ {
				slot := ((day-1)*1440 + start + i) % len(occupied)
				if occupied[slot] {
					return fmt.Errorf("overlapping price windows")
				}
				occupied[slot] = true
			}
		}
	}
	return nil
}

// Prices are frozen when a queued attempt is admitted, not when its stream ends.
func (p Profile) priceAt(at time.Time) Profile {
	s := p.Pricing
	p.Pricing = nil
	p.AppliedPrice = &AppliedPrice{Label: "固定价格", WindowID: "default", Timezone: "UTC", PricedAt: at.UTC()}
	if s == nil {
		return p
	}
	p.AppliedPrice.Label = s.DefaultLabel
	p.AppliedPrice.Timezone = s.Timezone
	zone, _ := time.LoadLocation(s.Timezone)
	calendarZone := zone
	if s.CalendarTimezone != "" {
		calendarZone, _ = time.LoadLocation(s.CalendarTimezone)
	}
	date := at.In(calendarZone).Format("2006-01-02")
	for _, excluded := range s.ExcludedDates {
		if excluded == date {
			p.AppliedPrice.WindowID = "excluded_date"
			return p
		}
	}
	local := at.In(zone)
	day := (int(local.Weekday())+6)%7 + 1
	minute := local.Hour()*60 + local.Minute()
	for _, w := range s.Windows {
		start, _ := clockMinute(w.Start, false)
		end, _ := clockMinute(w.End, true)
		for _, d := range w.Days {
			match := day == d && minute >= start && minute < end
			if end < start {
				match = day == d && minute >= start || day == d%7+1 && minute < end
			}
			if !match {
				continue
			}
			p.InputUSDPerMillion = w.InputUSDPerMillion
			p.OutputUSDPerMillion = w.OutputUSDPerMillion
			p.CachedInputUSDPerMillion = w.CachedInputUSDPerMillion
			p.ImageUSDPerImage = w.ImageUSDPerImage
			p.AppliedPrice.Label = w.Label
			p.AppliedPrice.WindowID = w.ID
			return p
		}
	}
	return p
}

func usageCostMicros(p Profile, u tokenUsage) int64 {
	cached := int64(0)
	rate := p.InputUSDPerMillion
	if p.CachedInputUSDPerMillion != nil && u.cachedKnown {
		cached = u.cached
		rate = *p.CachedInputUSDPerMillion
	}
	return int64(math.Ceil(float64(u.input-cached)*p.InputUSDPerMillion + float64(cached)*rate + float64(u.output)*p.OutputUSDPerMillion + p.ImageUSDPerImage*1e6))
}

// Only administrator-approved candidates compete; ties retain configured order.
func cheapestRoute(c Config, scene Scene, ids []string, raw []byte, at time.Time) string {
	best := ids[0]
	cheapest := int64(math.MaxInt64)
	if scene.RoutingPolicy != "lowest_cost" {
		return best
	}
	for _, id := range ids {
		p := c.Profile(id).priceAt(at)
		_, input, output, _, err := prepareRequest(raw, scene.Endpoint, p)
		if err != nil {
			continue
		}
		cost := costMicros(p, input, output)
		if cost < cheapest {
			best = id
			cheapest = cost
		}
	}
	return best
}
