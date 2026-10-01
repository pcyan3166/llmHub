package llmhub

import "testing"

func TestProjectCredentialsPrecedenceAndValidation(t *testing.T) {
	c := testConfig()
	p, source := c.resolvedProfile("storepilot", "text-fast")
	if p.KeyName != "primary" || p.PoolID != "shared" || source != "profile_default" {
		t.Fatal(p, source)
	}
	c.Pools = append(c.Pools, Pool{ID: "dedicated", Concurrency: 1})
	c.DefaultCredentials = []ProviderCredential{{Provider: "openai", KeyName: "global", PoolID: "shared"}}
	c.Projects[0].Credentials = []ProviderCredential{{Provider: "openai", KeyName: "project", PoolID: "dedicated"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	p, source = c.resolvedProfile("storepilot", "text-fast")
	if p.KeyName != "project" || p.PoolID != "dedicated" || source != "project" {
		t.Fatal(p, source)
	}
	c.Projects[0].Credentials = []ProviderCredential{{Provider: "deepseek", KeyName: "other", PoolID: "shared"}}
	p, source = c.resolvedProfile("storepilot", "text-fast")
	if p.KeyName != "global" || source != "default" {
		t.Fatal("partial overrides suppressed default", p, source)
	}
	if c.Profiles[0].KeyName != "primary" || c.Profiles[0].PoolID != "shared" {
		t.Fatal("resolution mutated config")
	}
	for _, bindings := range [][]ProviderCredential{
		{{Provider: "openai", KeyName: "x", PoolID: "absent"}},
		{{Provider: "openai", KeyName: "x", PoolID: "shared"}, {Provider: "openai", KeyName: "y", PoolID: "shared"}},
		{{Provider: "bad/provider", KeyName: "x", PoolID: "shared"}},
		{{Provider: "openai", KeyName: "", PoolID: "shared"}},
		{{Provider: "openai", KeyName: "key\r\nAuthorization: bypass", PoolID: "shared"}},
		{{Provider: "openai", KeyName: "key\t", PoolID: "shared"}},
		{{Provider: "openai", KeyName: " key", PoolID: "shared"}},
	} {
		c.Projects[0].Credentials = bindings
		if c.Validate() == nil {
			t.Fatal("bad project binding accepted", bindings)
		}
		c.Projects[0].Credentials = nil
		c.DefaultCredentials = bindings
		if c.Validate() == nil {
			t.Fatal("bad default binding accepted", bindings)
		}
	}
}
