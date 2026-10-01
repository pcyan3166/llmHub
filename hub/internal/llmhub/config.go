package llmhub

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

type Config struct {
	Projects           []Project            `json:"projects"`
	Profiles           []Profile            `json:"profiles"`
	Scenes             []Scene              `json:"scenes"`
	Pools              []Pool               `json:"pools"`
	Catalog            *CatalogSettings     `json:"catalog,omitempty"`
	DefaultCredentials []ProviderCredential `json:"default_credentials,omitempty"`
}

type Project struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Enabled          bool                 `json:"enabled"`
	MonthlyBudgetUSD float64              `json:"monthly_budget_usd"`
	Credentials      []ProviderCredential `json:"credentials,omitempty"`
}

// Secrets remain in Bifrost. A binding selects a named upstream key and its
// actual quota pool; creating another key never creates another account quota.
type ProviderCredential struct {
	Provider string `json:"provider"`
	KeyName  string `json:"key_name"`
	PoolID   string `json:"pool_id"`
}

type Profile struct {
	ID                       string         `json:"id"`
	Provider                 string         `json:"provider"`
	Model                    string         `json:"model"`
	KeyName                  string         `json:"key_name"`
	PoolID                   string         `json:"pool_id"`
	MaxOutputTokens          int64          `json:"max_output_tokens"`
	InputUSDPerMillion       float64        `json:"input_usd_per_million"`
	OutputUSDPerMillion      float64        `json:"output_usd_per_million"`
	ImageUSDPerImage         float64        `json:"image_usd_per_image,omitempty"`
	ImageSize                string         `json:"image_size,omitempty"`
	ImageQuality             string         `json:"image_quality,omitempty"`
	CachedInputUSDPerMillion *float64       `json:"cached_input_usd_per_million,omitempty"`
	Pricing                  *PriceSchedule `json:"pricing,omitempty"`
	AppliedPrice             *AppliedPrice  `json:"applied_price,omitempty"`
	FollowOfficial           bool           `json:"follow_official,omitempty"`
	OfficialTerms            string         `json:"official_terms,omitempty"`
	OfficialCalendarYear     int            `json:"official_calendar_year,omitempty"`
	OfficialCalendarHash     string         `json:"official_calendar_hash,omitempty"`
}

type Scene struct {
	ID                  string   `json:"id"`
	ProjectID           string   `json:"project_id"`
	Name                string   `json:"name"`
	Profiles            []string `json:"profiles"`
	Endpoint            string   `json:"endpoint"`
	QueueTimeoutSeconds int      `json:"queue_timeout_seconds"`
	TimeoutSeconds      int      `json:"timeout_seconds"`
	Retries             int      `json:"retries"`
	RoutingPolicy       string   `json:"routing_policy,omitempty"`
}

type Pool struct {
	ID          string `json:"id"`
	Concurrency int    `json:"concurrency"`
	QueueSize   int    `json:"queue_size"`
	RPM         int    `json:"rpm"`
	TPM         int64  `json:"tpm"`
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func (c Config) Validate() error {
	if c.Catalog != nil && (c.Catalog.IntervalMinutes < 15 || c.Catalog.IntervalMinutes > 10080) {
		return fmt.Errorf("catalog interval must be between 15 and 10080 minutes")
	}
	if len(c.Projects) > 1000 || len(c.Profiles) > 1000 || len(c.Scenes) > 5000 || len(c.Pools) > 1000 {
		return fmt.Errorf("configuration exceeds entity limits")
	}
	projects, profiles, pools, scenes := map[string]bool{}, map[string]Profile{}, map[string]bool{}, map[string]bool{}
	for _, p := range c.Projects {
		if !idPattern.MatchString(p.ID) || projects[p.ID] || strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 || !validPrice(p.MonthlyBudgetUSD) {
			return fmt.Errorf("invalid or duplicate project %q", p.ID)
		}
		projects[p.ID] = true
	}
	for _, p := range c.Pools {
		if !idPattern.MatchString(p.ID) || pools[p.ID] || p.Concurrency < 1 || p.Concurrency > 256 || p.QueueSize < 0 || p.QueueSize > 10000 || p.RPM < 0 || p.RPM > 1000000 || p.TPM < 0 || p.TPM > 1000000000 {
			return fmt.Errorf("invalid or duplicate pool %q", p.ID)
		}
		pools[p.ID] = true
	}
	if err := validateCredentials(c.DefaultCredentials, pools); err != nil {
		return fmt.Errorf("default credentials: %w", err)
	}
	for _, p := range c.Projects {
		if err := validateCredentials(p.Credentials, pools); err != nil {
			return fmt.Errorf("project %q credentials: %w", p.ID, err)
		}
	}
	for _, p := range c.Profiles {
		if p.FollowOfficial && (!supportedCatalogProvider(p.Provider) || p.ImageUSDPerImage > 0) || p.OfficialTerms != "" && !catalogHashPattern.MatchString(p.OfficialTerms) || p.OfficialCalendarHash != "" && !catalogHashPattern.MatchString(p.OfficialCalendarHash) || p.OfficialCalendarYear != 0 && (p.OfficialCalendarYear < 2000 || p.OfficialCalendarYear > 2200) {
			return fmt.Errorf("invalid official price tracking for profile %q", p.ID)
		}
		_, duplicate := profiles[p.ID]
		if !idPattern.MatchString(p.ID) || duplicate || !pools[p.PoolID] || !idPattern.MatchString(p.Provider) || p.Model == "" || len(p.Model) > 256 || strings.ContainsAny(p.Model+p.KeyName, "\r\n") || p.KeyName == "" || len(p.KeyName) > 128 || p.MaxOutputTokens < 1 || p.MaxOutputTokens > 1000000 || !validPrice(p.InputUSDPerMillion) || !validPrice(p.OutputUSDPerMillion) {
			return fmt.Errorf("invalid or duplicate profile %q", p.ID)
		}
		profiles[p.ID] = p
		if err := p.validatePricing(); err != nil {
			return fmt.Errorf("profile %q: %w", p.ID, err)
		}
		if !validPrice(p.ImageUSDPerImage) || p.ImageUSDPerImage > 0 && (p.ImageSize == "" || p.ImageQuality == "" || len(p.ImageSize) > 32 || len(p.ImageQuality) > 32) {
			return fmt.Errorf("invalid image tariff for profile %q", p.ID)
		}
	}
	for _, s := range c.Scenes {
		if s.RoutingPolicy != "" && s.RoutingPolicy != "ordered" && s.RoutingPolicy != "lowest_cost" {
			return fmt.Errorf("invalid routing policy in scene %q", s.ID)
		}
		key := s.ProjectID + "/" + s.ID
		if !idPattern.MatchString(s.ID) || scenes[key] || !projects[s.ProjectID] || s.Name == "" || len(s.Name) > 128 || len(s.Profiles) < 1 || len(s.Profiles) > 8 || s.TimeoutSeconds < 1 || s.TimeoutSeconds > 900 || s.QueueTimeoutSeconds < 1 || s.QueueTimeoutSeconds > s.TimeoutSeconds || s.Retries < 0 || s.Retries > 3 {
			return fmt.Errorf("invalid or duplicate scene %q", key)
		}
		if s.Endpoint != "/v1/chat/completions" && s.Endpoint != "/v1/responses" && s.Endpoint != "/v1/embeddings" && s.Endpoint != "/v1/images/generations" {
			return fmt.Errorf("unsupported endpoint in scene %q", key)
		}
		seen := map[string]bool{}
		for _, id := range s.Profiles {
			if _, ok := profiles[id]; !ok || seen[id] {
				return fmt.Errorf("invalid profile %q in scene %q", id, key)
			}
			seen[id] = true
			if (s.Endpoint == "/v1/images/generations") != (profiles[id].ImageUSDPerImage > 0) {
				return fmt.Errorf("profile %q must match the scene's text or image tariff", id)
			}
		}
		scenes[key] = true
	}
	return nil
}

func validateCredentials(bindings []ProviderCredential, pools map[string]bool) error {
	if len(bindings) > 64 {
		return fmt.Errorf("too many provider bindings")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !idPattern.MatchString(b.Provider) || seen[b.Provider] || strings.TrimSpace(b.KeyName) != b.KeyName || b.KeyName == "" || len(b.KeyName) > 128 || strings.IndexFunc(b.KeyName, func(r rune) bool { return r < 32 || r == 127 }) >= 0 || !pools[b.PoolID] {
			return fmt.Errorf("invalid or duplicate provider binding %q", b.Provider)
		}
		seen[b.Provider] = true
	}
	return nil
}

func (c Config) resolvedProfile(projectID, profileID string) (Profile, string) {
	p := c.Profile(profileID)
	project, _ := c.Project(projectID)
	for _, group := range []struct {
		bindings []ProviderCredential
		source   string
	}{{project.Credentials, "project"}, {c.DefaultCredentials, "default"}} {
		for _, b := range group.bindings {
			if b.Provider == p.Provider {
				p.KeyName, p.PoolID = b.KeyName, b.PoolID
				return p, group.source
			}
		}
	}
	return p, "profile_default"
}

func validPrice(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1000000 }

func (c Config) Project(id string) (Project, bool) {
	for _, p := range c.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

func (c Config) Scene(project, id string) (Scene, bool) {
	for _, s := range c.Scenes {
		if s.ProjectID == project && s.ID == id {
			return s, true
		}
	}
	return Scene{}, false
}

func (c Config) Profile(id string) Profile {
	for _, p := range c.Profiles {
		if p.ID == id {
			return p
		}
	}
	return Profile{}
}

// Prices are configured explicitly; integer microdollars avoid floating point accumulation.
func costMicros(p Profile, input, output int64) int64 {
	return int64(math.Ceil(float64(input)*p.InputUSDPerMillion + float64(output)*p.OutputUSDPerMillion + p.ImageUSDPerImage*1e6))
}
