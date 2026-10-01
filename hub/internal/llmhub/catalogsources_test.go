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
	for _, source := range officialSources {
		provider := source.Provider
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

func providerPage(provider string) string {
	switch provider {
	case "glm":
		return "# Pricing\nAll prices are in USD.\n### Text Models\nPrices per 1M tokens.\n| Model | Input | Cached Input | Cached Input Storage | Output |\n| :- | :- | :- | :- | :- |\n| GLM-test | \\$2 | \\$0.2 | Limited-time Free | \\$4 |\n"
	case "minimax":
		return "# Pay as You Go\n## LLM\n| Model | Input | Output | Prompt caching Read | Prompt caching Write |\n| :- | :- | :- | :- | :- |\n| **MiniMax-test** | \\$2 / M tokens | \\$4 / M tokens | \\$0.2 / M tokens | \\$2.5 / M tokens |\n"
	case "kimi":
		return "# Model Inference Pricing Explanation\n1M = 1,000,000\n" + `<DocTable columns={[{ title: "Model", width: "20%" },{ title: "Unit", width: "20%" },{ title: "Input Price (Cache Hit)", width: "20%" },{ title: "Input Price (Cache Miss)", width: "20%" },{ title: "Output Price", width: "10%" },{ title: "Context Window", width: "10%" },]} rows={[["kimi-test", "1M tokens", <>{"$"}0.2</>, <>{"$"}2</>, <>{"$"}4</>, "128K"],]} />`
	case "gemini":
		return `<div class="devsite-article-body"><h1>Gemini Developer API pricing</h1><h2 id="gemini-test">Gemini Test</h2><section><h3>Standard</h3><table class="pricing-table"><tr><th></th><th>Free Tier</th><th>Paid Tier, per 1M tokens in USD</th></tr><tr><td>Input price</td><td>Free of charge</td><td>$2 through December 31; $4 next year</td></tr><tr><td>Output price</td><td>Free of charge</td><td>$6</td></tr></table></section></div>`
	case "qwen":
		return `<article class="markdown-body"><h1>Model pricing</h1><h2>Tiered pricing rules</h2><h3>Qwen-Max</h3><section id="singapore"><table><tr><th>Model ID</th><th>Deployment scope</th><th>Mode</th><th>Input tokens per request</th><th>Input price (per 1 million tokens)</th><th>Output price (per 1 million tokens)</th></tr><tr><td rowspan="2">qwen-test</td><td rowspan="2">International</td><td rowspan="2">Thinking</td><td>0–32K</td><td>$2</td><td>$4</td></tr><tr><td>32K–128K</td><td>$4</td><td>$8</td></tr></table></section></article>`
	}
	return ""
}

func TestAdditionalProviderPricingSnapshots(t *testing.T) {
	for _, provider := range []string{"glm", "minimax", "kimi", "gemini", "qwen"} {
		t.Run(provider, func(t *testing.T) {
			page := providerPage(provider)
			quotes, terms, err := parseCatalog(provider, []byte(page))
			if err != nil || len(quotes) != 1 || len(terms) != 64 {
				t.Fatalf("%+v %v", quotes, err)
			}
			q := quotes[0]
			if q.Automatic || len(q.BillingDetails) == 0 {
				t.Fatal("lost billing dimensions or enabled unsafe automatic tariff")
			}
			if _, err := officialTariff(Profile{}, q, instant("2026-10-01T00:00:00Z"), true); err == nil {
				t.Fatal("complex tariff applied as a scalar")
			}
			if provider == "qwen" || provider == "gemini" {
				if !q.DisplayOnly {
					t.Fatal("complex quote presented as zero-priced model")
				}
			} else if q.DisplayOnly || q.Input != 2 || q.Output != 4 || *q.Cached != 0.2 {
				t.Fatal("incorrect USD / 1M quote", q)
			}
			changedPage := strings.ReplaceAll(page, "$2", "$3")
			if provider == "kimi" {
				changedPage = strings.ReplaceAll(page, `"}2</>`, `"}3</>`)
			}
			changed, changedTerms, err := parseCatalog(provider, []byte(changedPage))
			if err != nil || len(catalogChanges(CatalogSource{Quotes: quotes, TermsHash: terms}, CatalogSource{Provider: provider, Quotes: changed, TermsHash: changedTerms}, instant("2026-10-01T00:00:00Z"))) == 0 {
				t.Fatal("price update not detected", err)
			}
			if _, _, err := parseCatalog(provider, []byte("login required")); err == nil {
				t.Fatal("accepted login instead of official document")
			}
		})
	}
	page := strings.ReplaceAll(providerPage("glm"), "\\$2", "Free")
	q, _, err := parseCatalog("glm", []byte(page))
	if err == nil || q != nil {
		t.Fatal("accepted cached input greater than free input")
	}
	for _, provider := range []string{"glm", "minimax", "kimi", "gemini", "qwen"} {
		page := strings.ReplaceAll(providerPage(provider), "$", "¥")
		page = strings.ReplaceAll(page, "USD", "CNY")
		if _, _, err := parseCatalog(provider, []byte(page)); err == nil {
			t.Fatal("accepted wrong currency", provider)
		}
	}
}

func TestProviderBillingDimensionsAndLiteralSafety(t *testing.T) {
	page := providerPage("minimax")
	row := "| **MiniMax-test** | \\$2 / M tokens | \\$4 / M tokens | \\$0.2 / M tokens | \\$2.5 / M tokens |"
	page = strings.Replace(page, row, "<Tabs>\n<Tab title=\"Standard\">\n"+row+"\n</Tab>\n<Tab title=\"Priority*\">\n"+strings.ReplaceAll(row, "\\$2 /", "~~\\$4~~ \\$3 /")+"\n</Tab>\n</Tabs>", 1)
	quotes, _, err := parseCatalog("minimax", []byte(page))
	if err != nil || len(quotes) != 2 || quotes[0].Input != 2 || quotes[1].Input != 3 || !strings.Contains(quotes[1].Name, "Priority*") || quotes[1].BillingDetails["Prompt caching Write"] != "$2.5 / M tokens" {
		t.Fatal("priority, discount or write price lost", quotes, err)
	}
	for _, page := range []string{strings.ReplaceAll(providerPage("minimax"), "**MiniMax-test**", ""), strings.ReplaceAll(providerPage("glm"), "Cached Input Storage", "Hourly storage bytes")} {
		provider := "minimax"
		if strings.Contains(page, "# Pricing") {
			provider = "glm"
		}
		if _, _, err := parseCatalog(provider, []byte(page)); err == nil {
			t.Fatal("accepted empty model or unknown dimensions")
		}
	}
	free := strings.ReplaceAll(providerPage("glm"), `\$2`, "Free")
	free = strings.ReplaceAll(free, `\$0.2`, "Free")
	free = strings.ReplaceAll(free, `\$4`, "Free")
	quotes, _, err = parseCatalog("glm", []byte(free))
	if err != nil || quotes[0].Input != 0 || quotes[0].Output != 0 || *quotes[0].Cached != 0 || quotes[0].DisplayOnly {
		t.Fatal("legitimate free tariff lost", err)
	}
	for _, value := range []string{`fetch("https://example.invalid")`, `undefined`, `<>{"CNY"}2</>`} {
		page := strings.Replace(providerPage("kimi"), `<>{"$"}2</>`, value, 1)
		if _, _, err := parseCatalog("kimi", []byte(page)); err == nil {
			t.Fatal("accepted executable MDX or wrong currency", value)
		}
	}
	qwen := strings.Replace(providerPage("qwen"), `<tr><th>Model ID</th>`, `<thead><tr><th rowspan="2">Model ID</th>`, 1)
	qwen = strings.Replace(qwen, `<th>Deployment scope</th><th>Mode</th><th>Input tokens per request</th><th>Input price (per 1 million tokens)</th><th>Output price (per 1 million tokens)</th></tr>`, `<th rowspan="2">Deployment scope</th><th rowspan="2">Mode</th><th rowspan="2">Input tokens per request</th><th rowspan="2">Input price (per 1 million tokens)</th><th>Output price (per 1 million tokens)</th></tr><tr><th>Thinking mode</th></tr></thead>`, 1)
	quotes, _, err = parseCatalog("qwen", []byte(qwen))
	if err != nil || len(quotes) != 1 {
		t.Fatal(err, quotes)
	}
	outputModes := 0
	for dimension, price := range quotes[0].BillingDetails {
		if strings.Contains(dimension, "Output price (per 1 million tokens) / Thinking mode") {
			outputModes++
			if price != "$4" && price != "$8" {
				t.Fatal("tiered output price lost", price)
			}
		}
	}
	if outputModes != 2 {
		t.Fatal("multi-row headers or tiered rows lost", quotes[0])
	}
	gemini := strings.Replace(providerPage("gemini"), `<td>Free of charge</td>`, `<td></td>`, 1)
	quotes, _, err = parseCatalog("gemini", []byte(gemini))
	if err != nil || !quotes[0].DisplayOnly {
		t.Fatal("blank free tier incorrectly treated as paid zero", err)
	}
}
