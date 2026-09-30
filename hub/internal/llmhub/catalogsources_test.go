package llmhub

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func openAIPage(input string) string {
	return "# Pricing\nPrices per 1M tokens.\n### Standard pricing data\n" +
		"| Model | Short context input | Short context cached input | Short context cache writes | Short context output | Long context input | Long context cached input | Long context cache writes | Long context output |\n" +
		"| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n" +
		"| test-model | $" + input + " | $0.1 | - | $2 | - | - | - | - |\n" +
		"| test-conditional | $3 | $0.2 | $3.75 | $4 | $6 | $0.4 | $7.5 | $8 |\n" +
		"### Batch pricing data\n| test-model | $0.5 | $0.05 | - | $1 | - | - | - | - |\n"
}

func deepSeekPage(input string) string {
	return `<article><p>The prices are per 1M tokens.</p><table>` +
		`<tr><td colspan="3">MODEL</td><td>test-model<sup>(1)</sup></td></tr>` +
		`<tr><td rowspan="6">PRICING<sup>(2)</sup></td><td rowspan="2">1M INPUT TOKENS<br>(CACHE HIT)</td><td>OFF-PEAK</td><td>$0.1</td></tr>` +
		`<tr><td>PEAK</td><td>$0.2</td></tr>` +
		`<tr><td rowspan="2">1M INPUT TOKENS<br>(CACHE MISS)</td><td>OFF-PEAK</td><td>$` + input + `</td></tr>` +
		`<tr><td>PEAK</td><td>$4</td></tr>` +
		`<tr><td rowspan="2">1M OUTPUT TOKENS</td><td>OFF-PEAK</td><td>$3</td></tr>` +
		`<tr><td>PEAK</td><td>$6</td></tr></table>` +
		`<p>Peak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC, Monday through Friday, excluding Chinese public holidays. All other hours are off-peak, including weekends and Chinese public holidays in full.</p></article>`
}

func anthropicPage() string {
	return "All prices are in USD.\n## Model pricing\n" +
		"| Model | Base input tokens | 5m cache writes | 1h cache writes | Cache hits and refreshes | Output tokens |\n" +
		"| :--- | :--- | :--- | :--- | :--- | :--- |\n" +
		"| Claude Test 1 | $2 / MTok | $2.50 / MTok | $4 / MTok | $0.2 / MTok<sup>1</sup> | $10 / MTok |\n## Cloud platform pricing\n"
}

func TestOfficialParsersRejectAmbiguityAndBindConditions(t *testing.T) {
	for _, provider := range []string{"deepseek", "openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			page := openAIPage("2")
			if provider == "deepseek" {
				page = deepSeekPage("2")
			}
			if provider == "anthropic" {
				page = anthropicPage()
			}
			quotes, terms, err := parseCatalog(provider, []byte(page))
			if err != nil || len(quotes) < 1 || quotes[0].Input != 2 || len(terms) != 64 {
				t.Fatalf("%+v %s %v", quotes, terms, err)
			}
			if provider == "anthropic" && (quotes[0].Automatic || quotes[0].Model != "") {
				t.Fatal("marketing names must not be guessed as API IDs")
			}
			if provider == "openai" && (!quotes[0].Automatic || quotes[1].Automatic || quotes[0].Output != 2) {
				t.Fatal("mixed up batch, long context or cache writes")
			}
			if provider == "deepseek" && (!quotes[0].Automatic || quotes[0].Peak.Input != 4 || *quotes[0].Peak.Cached != 0.2) {
				t.Fatal("rowspans or peak tariffs were lost")
			}
			_, changedTerms, err := parseCatalog(provider, []byte(page+"\nAdditional billing conditions apply."))
			if provider == "deepseek" {
				_, changedTerms, err = parseCatalog(provider, []byte(strings.Replace(page, "</article>", "<p>Additional billing conditions apply.</p></article>", 1)))
			}
			if err != nil || changedTerms == terms {
				t.Fatal("new conditions did not invalidate approvals")
			}
			for _, mutation := range []string{strings.ReplaceAll(page, "$", "€"), strings.ReplaceAll(page, "INPUT TOKENS", "INPUT CHARACTERS"), strings.ReplaceAll(page, "Short context input", "Hourly input"), strings.ReplaceAll(page, "Base input tokens", "Base input bytes")} {
				if mutation == page {
					continue
				}
				if _, _, err := parseCatalog(provider, []byte(mutation)); err == nil {
					t.Fatal("accepted incompatible billing columns or currency")
				}
			}
		})
	}
	a, terms, err := parseCatalog("openai", []byte(openAIPage("1")))
	if err != nil {
		t.Fatal(err)
	}
	b, next, err := parseCatalog("openai", []byte(openAIPage("2")))
	if err != nil || next != terms || a[0].Input == b[0].Input {
		t.Fatal("pure price change must preserve approval terms")
	}
	changed, _, err := parseCatalog("openai", []byte(strings.Replace(openAIPage("1"), "$7.5", "$9", 1)))
	if err != nil || changed[1].BillingDetails["Long context cache writes"] != "$9" {
		t.Fatal("conditional price change was lost")
	}
	events := catalogChanges(CatalogSource{Quotes: a}, CatalogSource{Provider: "openai", Quotes: changed}, instant("2026-10-01T00:00:00Z"))
	if len(events) != 1 || events[0].Kind != "price_changed" || events[0].Model != "test-conditional" {
		t.Fatal("conditional price change did not produce an audit event", events)
	}
	if _, _, err := parseCatalog("openai", []byte(strings.Replace(openAIPage("2"), "$0.1", "$9", 1))); err == nil {
		t.Fatal("cache exceeds input")
	}
	if _, _, err := parseCatalog("deepseek", []byte(strings.Replace(deepSeekPage("2"), `rowspan="6"`, `rowspan="999"`, 1))); err == nil {
		t.Fatal("unbounded HTML span")
	}
	q, _, err := parseCatalog("deepseek", []byte(strings.Replace(deepSeekPage("2"), "01:00 - 04:00", "02:00 - 04:00", 1)))
	if err != nil || q[0].Automatic {
		t.Fatal("changed peak rules may not auto-apply")
	}
	if _, _, err := parseCatalog("openai", []byte("login required")); err == nil {
		t.Fatal("unrecognized page")
	}
}

// Opt-in smoke against previously downloaded public pages; no network or paid inference in tests.
func TestOfficialDownloadedPages(t *testing.T) {
	for _, provider := range []string{"openai", "deepseek", "anthropic"} {
		path := os.Getenv("LLMHUB_OFFICIAL_" + strings.ToUpper(provider) + "_PAGE")
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		quotes, _, err := parseCatalog(provider, raw)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		t.Log(fmt.Sprintf("%s: %d official model quotes, first %s", provider, len(quotes), quotes[0].Name))
		news := os.Getenv("LLMHUB_OFFICIAL_" + strings.ToUpper(provider) + "_NEWS")
		if news != "" {
			raw, err := os.ReadFile(news)
			if err != nil {
				t.Fatal(err)
			}
			models, _, err := parseCatalogNews(provider, raw)
			if err != nil {
				t.Fatal(provider, err)
			}
			t.Logf("%s: %d model identifiers mentioned in official announcements", provider, len(models))
		}
	}
}

func TestOfficialAnnouncementsDoNotCreatePriceQuotes(t *testing.T) {
	for _, source := range officialSources {
		raw := fixtureNews(source.NewsURL)
		models, hash, err := parseCatalogNews(source.Provider, raw)
		if err != nil || len(models) != 1 || len(hash) != 64 {
			t.Fatal(source.Provider, models, err)
		}
		if _, _, err := parseCatalog(source.Provider, raw); err == nil {
			t.Fatal("announcement parsed as a verified price")
		}
		if _, _, err := parseCatalogNews(source.Provider, []byte("login required")); err == nil {
			t.Fatal("unrecognized official news accepted")
		}
	}
}
