package llmhub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var markdownSeparator = regexp.MustCompile(`^:?-+:?$`)
var providerDollar = regexp.MustCompile(`^(?:~~\$[0-9]+(?:\.[0-9]+)?~~ )?\$([0-9]+(?:\.[0-9]+)?)(?: / M tokens)?$`)
var providerModel = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.-]{0,127}$`)

func providerPrice(value string) (float64, error) {
	value = strings.ReplaceAll(value, `\$`, "$")
	if value == "Free" {
		return 0, nil
	}
	m := providerDollar.FindStringSubmatch(value)
	if m == nil {
		return 0, fmt.Errorf("unrecognized USD token price")
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || !validPrice(v) {
		return 0, fmt.Errorf("invalid USD token price")
	}
	return v, nil
}

func tableCells(line string) []string {
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func parseProviderMarkdown(provider, raw string) ([]CatalogQuote, string, error) {
	heading := "# Pricing\n"
	if provider == "minimax" {
		heading = "# Pay as You Go\n"
	}
	if !strings.Contains(raw, heading) || provider == "glm" && (!strings.Contains(raw, "All prices are in USD.") || !strings.Contains(raw, "Prices per 1M tokens.")) || provider == "minimax" && !strings.Contains(raw, "/ M tokens") {
		return nil, "", fmt.Errorf("official pricing heading, currency or token unit changed")
	}
	quotes, terms := []CatalogQuote{}, []string{}
	var headers []string
	active, tier, section := false, "", ""
	for _, line := range strings.Split(raw, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "## ") || strings.HasPrefix(trim, "### ") {
			section, headers = trim, nil
			active = provider == "glm" && (trim == "### Latest Models" || trim == "### Text Models" || trim == "### Vision Models") || provider == "minimax" && trim == "## LLM"
		}
		if strings.HasPrefix(trim, "<Tab title=\"") {
			tier = strings.Split(strings.TrimPrefix(trim, "<Tab title=\""), "\"")[0]
		}
		if trim == "</Tabs>" {
			tier = ""
		}
		if !active || !strings.HasPrefix(trim, "|") {
			terms = append(terms, line)
			continue
		}
		if !strings.HasSuffix(trim, "|") || strings.Contains(trim, `\|`) {
			return nil, "", fmt.Errorf("official pricing table grammar changed")
		}
		cells := tableCells(trim)
		if cells[0] == "Model" {
			want := "Model|Input|Cached Input|Cached Input Storage|Output"
			if provider == "minimax" {
				want = "Model|Input|Output|Prompt caching Read"
				if len(cells) == 5 {
					want += "|Prompt caching Write"
				}
			}
			if strings.Join(cells, "|") != want {
				return nil, "", fmt.Errorf("official billing headers changed")
			}
			headers = cells
			terms = append(terms, line)
			continue
		}
		if headers == nil || len(cells) != len(headers) {
			return nil, "", fmt.Errorf("official billing columns changed in %s", section)
		}
		if markdownSeparator.MatchString(cells[0]) {
			continue
		}
		name := cells[0]
		if provider == "minimax" {
			fragment, err := html.Parse(strings.NewReader(strings.ReplaceAll(name, "**", "")))
			if err != nil {
				return nil, "", err
			}
			name = normalizedText(fragment)
		}
		fields := strings.Fields(name)
		if len(fields) == 0 {
			return nil, "", fmt.Errorf("empty official model label")
		}
		model := fields[0]
		if !providerModel.MatchString(model) || !strings.HasPrefix(strings.ToLower(model), provider+"-") {
			return nil, "", fmt.Errorf("official model label changed")
		}
		if tier != "" {
			name += " · " + tier
		}
		out, cache := 4, 2
		if provider == "minimax" {
			out, cache = 2, 3
		}
		input, err := providerPrice(cells[1])
		if err != nil {
			return nil, "", err
		}
		output, err := providerPrice(cells[out])
		if err != nil {
			return nil, "", err
		}
		q := CatalogQuote{Name: name, Model: model, Input: input, Output: output, Conditions: "国际站 USD / 1M Token；含缓存存储、写入、上下文或服务档位条件，需人工核对，不自动覆盖业务单价", BillingDetails: map[string]string{}}
		if cells[cache] != "-" && cells[cache] != `\\` {
			v, err := providerPrice(cells[cache])
			if err != nil {
				return nil, "", err
			}
			q.Cached = &v
		}
		q.BillingDetails["Model / tier"] = name
		for i := 1; i < len(headers); i++ {
			q.BillingDetails[headers[i]] = strings.ReplaceAll(cells[i], `\$`, "$")
		}
		quotes = append(quotes, q)
	}
	return quotes, strings.Join(terms, "\n"), nil
}

func attribute(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func normalizedText(n *html.Node) string { return strings.Join(strings.Fields(documentText(n)), " ") }

func hasOfficialHeading(raw []byte, expected string) bool {
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	for _, h := range findElements(root, "h1") {
		if strings.Contains(normalizedText(h), expected) {
			return true
		}
	}
	return false
}

// Preserve boundaries between block elements so neighboring model IDs cannot merge.
func documentText(n *html.Node) string {
	if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		b.WriteString(documentText(child))
		if child.Type == html.ElementNode {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func officialHTMLBody(provider string, raw []byte) (*html.Node, error) {
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var matches []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		class := " " + attribute(n, "class") + " "
		if provider == "gemini" && strings.Contains(class, " devsite-article-body ") || provider == "qwen" && n.Data == "article" && strings.Contains(class, " markdown-body ") {
			matches = append(matches, n)
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if len(matches) != 1 {
		return nil, fmt.Errorf("official document body changed")
	}
	return matches[0], nil
}

func parseProviderHTML(provider string, raw []byte) ([]CatalogQuote, string, error) {
	body, err := officialHTMLBody(provider, raw)
	if err != nil {
		return nil, "", err
	}
	text := normalizedText(body)
	heading := map[string]string{"gemini": "Gemini Developer API pricing", "qwen": "Tiered pricing rules"}[provider]
	if !strings.Contains(text, heading) && !hasOfficialHeading(raw, heading) {
		return nil, "", fmt.Errorf("official pricing heading changed")
	}
	quotes := []CatalogQuote{}
	// These multi-dimensional tariffs are snapshots, not a fabricated scalar rate.
	model, mode := "", ""
	positions := map[string]int{}
	var walk func(*html.Node) error
	walk = func(n *html.Node) error {
		if n.Data == "script" || n.Data == "style" {
			return nil
		}
		if n.Data == "h2" {
			model, mode = "", ""
			if provider == "gemini" && strings.HasPrefix(attribute(n, "id"), "gemini-") {
				model = attribute(n, "id")
			}
		}
		if n.Data == "h3" {
			mode = normalizedText(n)
		}
		if n.Data == "table" {
			if provider == "gemini" && (model == "" || !strings.Contains(" "+attribute(n, "class")+" ", " pricing-table ")) {
				return nil
			}
			grid, err := readHTMLTable(n, true)
			if err != nil {
				return err
			}
			if len(grid) < 2 {
				return nil
			}
			headerRows := 1
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Data == "thead" {
					headerRows = len(findElements(child, "tr"))
				}
			}
			if headerRows < 1 || headerRows >= len(grid) {
				return fmt.Errorf("official table header changed")
			}
			headers := append([]string(nil), grid[0]...)
			for _, row := range grid[1:headerRows] {
				if len(row) != len(headers) {
					return fmt.Errorf("official table header dimensions changed")
				}
				for col, value := range row {
					if value != headers[col] {
						headers[col] += " / " + value
					}
				}
			}
			if provider == "gemini" {
				if model == "" {
					return nil
				}
				if !strings.Contains(strings.Join(grid[0], " "), "USD") {
					return fmt.Errorf("official Gemini currency changed")
				}
			}
			if provider == "qwen" && !strings.Contains(strings.Join(grid[0], " "), "Input price (per 1 million tokens)") {
				return nil
			}
			for r, row := range grid[headerRows:] {
				id := model
				if provider == "qwen" {
					if len(row) == 0 {
						continue
					}
					ids := newsModelPattern.FindAllString(row[0], -1)
					if len(ids) != 1 || !strings.HasPrefix(strings.ToLower(ids[0]), "qwen") {
						continue
					}
					id = ids[0]
					for col, header := range headers {
						if !strings.Contains(header, "price (per 1 million tokens)") {
							continue
						}
						if col >= len(row) {
							return fmt.Errorf("official Qwen billing columns changed")
						}
						value := row[col]
						if strings.ContainsAny(value, "¥￥€") || strings.Contains(value, "CNY") || !strings.Contains(value, "$") && !strings.HasPrefix(value, "USD ") && value != "未列出" && value != "-" && value != "Not supported" && value != "Not applicable" && value != "Free" && value != "Limited-time free" {
							return fmt.Errorf("official Qwen USD price changed for %s: %s", id, value)
						}
					}
				}
				pos, exists := positions[id]
				if !exists {
					pos = len(quotes)
					positions[id] = pos
					quotes = append(quotes, CatalogQuote{Name: id, Model: id, DisplayOnly: true, Conditions: "USD 原始计费明细；地区、上下文阶梯、时间促销、缓存、模态及服务档位需人工核对，不自动覆盖业务单价", BillingDetails: map[string]string{}})
				}
				for col, value := range row {
					if col >= len(headers) {
						return fmt.Errorf("official billing columns changed")
					}
					header := strings.Join(strings.Fields(headers[col]), " ")
					key := fmt.Sprintf("%s / %s / %d / %s", ancestorRegion(n), mode, r+1, header)
					quotes[pos].BillingDetails[key] = strings.Join(strings.Fields(value), " ")
				}
			}
			return nil
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(body); err != nil {
		return nil, "", err
	}
	return quotes, text, nil
}

func ancestorRegion(n *html.Node) string {
	for parent := n.Parent; parent != nil; parent = parent.Parent {
		if parent.Data == "section" && attribute(parent, "id") != "" {
			return attribute(parent, "id")
		}
	}
	return "Developer API"
}

var kimiDocTable = regexp.MustCompile(`(?s)<DocTable\s+columns=\{\[(.*?)\]\}\s+rows=\{\[(.*?)\]\}\s*/>`)
var kimiColumn = regexp.MustCompile(`\{\s*title:\s*"([^"]+)",\s*width:\s*"[0-9]+%"\s*\},`)
var kimiPriceLiteral = regexp.MustCompile(`<>\{"\$"\}([0-9]+(?:\.[0-9]+)?)</>`)
var trailingArrayComma = regexp.MustCompile(`,\s*\]`)

// Only accept literal arrays and the documented price fragment. Never execute MDX/JavaScript.
func parseKimiMarkdown(raw string) ([]CatalogQuote, string, error) {
	if !strings.Contains(raw, "# Model Inference Pricing Explanation\n") || !strings.Contains(raw, "1M = 1,000,000") {
		return nil, "", fmt.Errorf("official Kimi pricing heading or unit changed")
	}
	tables := kimiDocTable.FindAllStringSubmatch(raw, -1)
	if len(tables) == 0 || len(tables) > 20 {
		return nil, "", fmt.Errorf("official Kimi literal table missing")
	}
	quotes := []CatalogQuote{}
	for _, table := range tables {
		headers := []string{}
		for _, column := range kimiColumn.FindAllStringSubmatch(table[1], -1) {
			headers = append(headers, column[1])
		}
		if strings.TrimSpace(kimiColumn.ReplaceAllString(table[1], "")) != "" {
			return nil, "", fmt.Errorf("official Kimi column expressions changed")
		}
		out, input, cache := 4, 3, 2
		want := "Model|Unit|Input Price (Cache Hit)|Input Price (Cache Miss)|Output Price|Context Window"
		if len(headers) == 8 {
			out, input, cache = 6, 5, 4
			want = "Model|Unit|Cache Write Price (TTL 5min)|Cache Write Price (TTL 1h)|Cached Input Price|Input Price|Output Price|Context Window"
		}
		if strings.Join(headers, "|") != want {
			return nil, "", fmt.Errorf("official Kimi billing headers changed")
		}
		literal := kimiPriceLiteral.ReplaceAllString(table[2], `"$$$1"`)
		literal = trailingArrayComma.ReplaceAllString("["+literal+"]", "]")
		var rows [][]string
		if err := json.Unmarshal([]byte(literal), &rows); err != nil {
			return nil, "", fmt.Errorf("official Kimi table is not a supported literal array")
		}
		for _, row := range rows {
			if len(row) != len(headers) || row[1] != "1M tokens" || !providerModel.MatchString(row[0]) || !strings.HasPrefix(row[0], "kimi-") {
				return nil, "", fmt.Errorf("official Kimi model or token unit changed")
			}
			q := CatalogQuote{Name: row[0], Model: row[0], Conditions: "Kimi 国际站 USD / 1M Token；需核对缓存写入 TTL、账户地区及额外费用，不自动覆盖业务单价", BillingDetails: map[string]string{}}
			var err error
			q.Input, err = parseDollar(row[input])
			if err != nil {
				return nil, "", err
			}
			q.Output, err = parseDollar(row[out])
			if err != nil {
				return nil, "", err
			}
			v, err := parseDollar(row[cache])
			if err != nil {
				return nil, "", err
			}
			q.Cached = &v
			for i, h := range headers {
				q.BillingDetails[h] = row[i]
			}
			quotes = append(quotes, q)
		}
	}
	return quotes, raw, nil
}
