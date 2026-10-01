package llmhub

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var catalogModelPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,255}$`)
var dollarPattern = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]+)?)(?: / MTok)?(?:<sup>[0-9]+</sup>)?$`)
var newsModelPattern = regexp.MustCompile(`(?i)\b(?:gpt-[a-z0-9.-]+|o[1-9](?:-[a-z0-9.-]+)?|claude-[a-z0-9.-]+|deepseek-[a-z0-9.-]+|glm-[a-z0-9.-]+|MiniMax-[a-z0-9.-]+|kimi-[a-z0-9.-]+|moonshot-[a-z0-9.-]+|gemini-[a-z0-9.-]+|qwen[a-z0-9.-]*)\b`)

func parseCatalogNews(provider string, raw []byte) ([]string, string, error) {
	if len(raw) == 0 || len(raw) > 2<<20 {
		return nil, "", fmt.Errorf("invalid official announcements size")
	}
	text := string(raw)
	switch provider {
	case "deepseek":
		root, err := html.Parse(bytes.NewReader(raw))
		if err != nil {
			return nil, "", err
		}
		articles := findElements(root, "article")
		if len(articles) != 1 {
			return nil, "", fmt.Errorf("official change log article missing")
		}
		text = htmlText(articles[0])
		if !strings.Contains(text, "Change Log") {
			return nil, "", fmt.Errorf("official change log heading changed")
		}
	case "openai":
		if !strings.HasPrefix(text, "# Changelog\n") {
			return nil, "", fmt.Errorf("official changelog heading changed")
		}
	case "anthropic":
		if !strings.Contains(text, "# Models overview\n") {
			return nil, "", fmt.Errorf("official model overview heading changed")
		}
	case "glm", "minimax", "kimi":
		heading := map[string]string{"glm": "# New Released\n", "minimax": "# Models\n", "kimi": "# Model List\n"}[provider]
		if !strings.Contains(text, heading) {
			return nil, "", fmt.Errorf("official model announcements heading changed")
		}
	case "gemini", "qwen":
		body, err := officialHTMLBody(provider, raw)
		if err != nil {
			return nil, "", err
		}
		text = documentText(body)
		heading := map[string]string{"gemini": "Release notes", "qwen": "Recommended models"}[provider]
		if !strings.Contains(text, heading) && !hasOfficialHeading(raw, heading) {
			return nil, "", fmt.Errorf("official model announcements heading changed")
		}
	default:
		return nil, "", fmt.Errorf("unsupported official announcements")
	}
	seen := map[string]bool{}
	models := []string{}
	for _, model := range newsModelPattern.FindAllString(text, -1) {
		model = strings.TrimRight(model, ".")
		lower := strings.ToLower(model)
		valid := strings.HasPrefix(lower, provider+"-")
		if provider == "openai" {
			valid = strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o")
		}
		if provider == "anthropic" {
			valid = strings.HasPrefix(model, "claude-")
		}
		if provider == "kimi" {
			valid = strings.HasPrefix(lower, "kimi-") || strings.HasPrefix(lower, "moonshot-")
		}
		if provider == "qwen" {
			valid = strings.HasPrefix(lower, "qwen")
		}
		if valid && !seen[model] {
			seen[model] = true
			models = append(models, model)
		}
	}
	if len(models) > 500 {
		return nil, "", fmt.Errorf("official announcement models exceed limits")
	}
	return models, digest([]byte(strings.Join(strings.Fields(text), " "))), nil
}

func parseDollar(cell string) (float64, error) {
	m := dollarPattern.FindStringSubmatch(strings.TrimSpace(cell))
	if m == nil {
		return 0, fmt.Errorf("unrecognized USD price cell")
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || !validPrice(v) {
		return 0, fmt.Errorf("invalid USD price")
	}
	return v, nil
}

func parseCatalog(provider string, raw []byte) ([]CatalogQuote, string, error) {
	if len(raw) == 0 || len(raw) > 2<<20 {
		return nil, "", fmt.Errorf("invalid official page size")
	}
	var quotes []CatalogQuote
	var terms string
	var err error
	switch provider {
	case "deepseek":
		quotes, terms, err = parseDeepSeek(raw)
	case "openai":
		quotes, terms, err = parseMarkdownCatalog(string(raw), "### Standard pricing data", []string{"Model", "Short context input", "Short context cached input", "Short context cache writes", "Short context output", "Long context input", "Long context cached input", "Long context cache writes", "Long context output"}, false)
	case "anthropic":
		quotes, terms, err = parseMarkdownCatalog(string(raw), "## Model pricing", []string{"Model", "Base input tokens", "5m cache writes", "1h cache writes", "Cache hits and refreshes", "Output tokens"}, true)
	case "glm", "minimax":
		quotes, terms, err = parseProviderMarkdown(provider, string(raw))
	case "kimi":
		quotes, terms, err = parseKimiMarkdown(string(raw))
	case "gemini", "qwen":
		quotes, terms, err = parseProviderHTML(provider, raw)
	default:
		err = fmt.Errorf("unsupported official provider")
	}
	if err != nil {
		return nil, "", err
	}
	if len(quotes) == 0 || len(quotes) > 500 {
		return nil, "", fmt.Errorf("official model table is missing or exceeds limits")
	}
	seen := map[string]bool{}
	for _, q := range quotes {
		if q.Name == "" || len(q.Name) > 256 || seen[q.Name] || q.Cached != nil && *q.Cached > q.Input {
			return nil, "", fmt.Errorf("ambiguous official model or cache price")
		}
		seen[q.Name] = true
	}
	return quotes, digest([]byte(strings.Join(strings.Fields(terms), " "))), nil
}

// This intentionally accepts only a narrow Markdown table grammar and exact billing headers.
func parseMarkdownCatalog(raw, section string, headers []string, anthropic bool) ([]CatalogQuote, string, error) {
	if anthropic && !strings.Contains(raw, "All prices are in USD") || !anthropic && !strings.Contains(raw, "Prices per 1M tokens.") {
		return nil, "", fmt.Errorf("official currency or token unit is unrecognized")
	}
	lines := strings.Split(raw, "\n")
	quotes := []CatalogQuote{}
	terms := []string{}
	inSection, table, finished := false, false, false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == section {
			if inSection || finished {
				return nil, "", fmt.Errorf("duplicate pricing section")
			}
			inSection = true
		} else if inSection && strings.HasPrefix(trim, "#") {
			inSection = false
			finished = true
		}
		if !inSection || !strings.HasPrefix(trim, "|") {
			terms = append(terms, line)
			continue
		}
		if !strings.HasSuffix(trim, "|") || strings.Contains(trim, `\|`) {
			return nil, "", fmt.Errorf("unrecognized pricing table grammar")
		}
		cells := strings.Split(strings.Trim(trim, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) != len(headers) {
			return nil, "", fmt.Errorf("official pricing columns changed")
		}
		if !table {
			for i, h := range headers {
				if cells[i] != h {
					return nil, "", fmt.Errorf("official billing headers changed")
				}
			}
			table = true
			terms = append(terms, line)
			continue
		}
		if strings.HasPrefix(cells[0], "---") || strings.HasPrefix(cells[0], ":---") {
			continue
		}
		input, err := parseDollar(cells[1])
		if err != nil {
			return nil, "", err
		}
		outIndex, cacheIndex := 4, 2
		if anthropic {
			outIndex = 5
			cacheIndex = 4
		}
		output, err := parseDollar(cells[outIndex])
		if err != nil {
			return nil, "", err
		}
		q := CatalogQuote{Name: cells[0], Input: input, Output: output, Conditions: "标准文本 Token 报价；需核对账户、地区、服务档位及额外工具费用"}
		q.BillingDetails = map[string]string{}
		for i := 1; i < len(headers); i++ {
			q.BillingDetails[headers[i]] = cells[i]
		}
		if cells[cacheIndex] != "-" {
			v, err := parseDollar(cells[cacheIndex])
			if err != nil {
				return nil, "", err
			}
			q.Cached = &v
		}
		if !anthropic {
			if catalogModelPattern.MatchString(q.Name) {
				q.Model = q.Name
			}
			q.Automatic = q.Model != "" && cells[3] == "-" && cells[5] == "-" && cells[6] == "-" && cells[7] == "-" && cells[8] == "-"
			if !q.Automatic {
				q.Conditions = "含长上下文、缓存写入或限定模型名称；不能自动套用单一价格"
			}
		} else {
			q.Conditions = "含缓存写入、地区及服务模式附加收费；名称不是精确 API ID，需人工核对"
		}
		quotes = append(quotes, q)
	}
	if !table {
		return nil, "", fmt.Errorf("official pricing section not found")
	}
	return quotes, strings.Join(terms, "\n"), nil
}

func htmlText(n *html.Node) string {
	if n.Type == html.ElementNode && (n.Data == "sup" || n.Data == "script" || n.Data == "style") {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	if n.Type == html.ElementNode && n.Data == "br" {
		return " "
	}
	var b strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		b.WriteString(htmlText(child))
	}
	return b.String()
}

func findElements(n *html.Node, name string) []*html.Node {
	var all []*html.Node
	if n.Type == html.ElementNode && n.Data == name {
		all = append(all, n)
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		all = append(all, findElements(child, name)...)
	}
	return all
}

func htmlTable(table *html.Node) ([][]string, error) {
	return readHTMLTable(table, false)
}

func readHTMLTable(table *html.Node, reviewOnly bool) ([][]string, error) {
	rows := findElements(table, "tr")
	if len(rows) > 512 {
		return nil, fmt.Errorf("official table exceeds limits")
	}
	grid := make([][]string, len(rows))
	for i := range grid {
		grid[i] = make([]string, 24)
	}
	for r, row := range rows {
		col := 0
		for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type != html.ElementNode || cell.Data != "td" && cell.Data != "th" {
				continue
			}
			for col < 24 && grid[r][col] != "" {
				col++
			}
			rs, cs := 1, 1
			for _, a := range cell.Attr {
				if a.Key == "rowspan" || a.Key == "colspan" {
					v, err := strconv.Atoi(a.Val)
					maxSpan := 24
					if reviewOnly && a.Key == "rowspan" {
						maxSpan = 512
					}
					if err != nil || v < 1 || v > maxSpan {
						return nil, fmt.Errorf("invalid official table span")
					}
					if a.Key == "rowspan" {
						rs = v
					} else {
						cs = v
					}
				}
			}
			if col+cs > 24 || r+rs > len(rows) {
				return nil, fmt.Errorf("invalid official table dimensions")
			}
			value := strings.Join(strings.Fields(htmlText(cell)), " ")
			if reviewOnly {
				value = normalizedText(cell)
				if value == "" {
					value = "未列出"
				}
			}
			if value == "" {
				return nil, fmt.Errorf("empty official table cell")
			}
			for y := r; y < r+rs; y++ {
				for x := col; x < col+cs; x++ {
					if grid[y][x] != "" {
						return nil, fmt.Errorf("overlapping official table cells")
					}
					grid[y][x] = value
				}
			}
			col += cs
		}
		for len(grid[r]) > 0 && grid[r][len(grid[r])-1] == "" {
			grid[r] = grid[r][:len(grid[r])-1]
		}
	}
	return grid, nil
}

func parseDeepSeek(raw []byte) ([]CatalogQuote, string, error) {
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	articles := findElements(root, "article")
	if len(articles) != 1 {
		return nil, "", fmt.Errorf("official pricing article changed")
	}
	article := articles[0]
	text := htmlText(article)
	if !strings.Contains(text, "per 1M tokens") {
		return nil, "", fmt.Errorf("official token unit changed")
	}
	rule := "Peak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC, Monday through Friday, excluding Chinese public holidays. All other hours are off-peak, including weekends and Chinese public holidays in full."
	knownRule := strings.Contains(strings.Join(strings.Fields(text), " "), rule)
	var grid [][]string
	for _, table := range findElements(article, "table") {
		rows, e := htmlTable(table)
		if e != nil {
			return nil, "", e
		}
		if len(rows) > 0 && len(rows[0]) >= 4 && rows[0][0] == "MODEL" {
			if grid != nil {
				return nil, "", fmt.Errorf("ambiguous model table")
			}
			grid = rows
		}
	}
	if len(grid) == 0 {
		return nil, "", fmt.Errorf("official model table missing")
	}
	models := grid[0][3:]
	quotes := make([]CatalogQuote, len(models))
	for i, model := range models {
		if !catalogModelPattern.MatchString(model) {
			return nil, "", fmt.Errorf("official API model ID changed")
		}
		quotes[i] = CatalogQuote{Name: model, Model: model, Peak: &CatalogQuote{}, Automatic: knownRule, Conditions: "UTC 周一至周五 01–04 / 06–10 为峰时，中国法定节假日全天谷价；需确认完整年度日历"}
	}
	seen := map[string]bool{}
	var terms strings.Builder
	for _, row := range grid[1:] {
		if row[0] != "PRICING" {
			terms.WriteString(strings.Join(row, "|"))
			continue
		}
		if len(row) != len(models)+3 {
			return nil, "", fmt.Errorf("official price columns changed")
		}
		key := row[1] + "/" + row[2]
		if seen[key] {
			return nil, "", fmt.Errorf("duplicate official price row")
		}
		seen[key] = true
		for i := range quotes {
			v, err := parseDollar(row[i+3])
			if err != nil {
				return nil, "", err
			}
			q := &quotes[i]
			if row[2] == "PEAK" {
				q = q.Peak
			} else if row[2] != "OFF-PEAK" {
				return nil, "", fmt.Errorf("official peak label changed")
			}
			switch row[1] {
			case "1M INPUT TOKENS (CACHE HIT)":
				q.Cached = &v
			case "1M INPUT TOKENS (CACHE MISS)":
				q.Input = v
			case "1M OUTPUT TOKENS":
				q.Output = v
			default:
				return nil, "", fmt.Errorf("official billing dimension changed")
			}
		}
	}
	if len(seen) != 6 {
		return nil, "", fmt.Errorf("official peak/off-peak table is incomplete")
	}
	for _, q := range quotes {
		if q.Peak.Cached == nil || q.Cached == nil || *q.Peak.Cached > q.Peak.Input {
			return nil, "", fmt.Errorf("invalid cache tariff")
		}
	}
	// Include every non-table condition in the approval fingerprint, not page navigation.
	var withoutTables func(*html.Node)
	withoutTables = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			return
		}
		if n.Type == html.TextNode {
			terms.WriteString(n.Data)
			terms.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			withoutTables(child)
		}
	}
	withoutTables(article)
	return quotes, terms.String(), nil
}
