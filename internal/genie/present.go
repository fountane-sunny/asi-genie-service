package genie

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"asi-genie-poc/internal/cards"
	genillm "asi-genie-poc/llm"
)

const presentTool = "present_answer"

const (
	agentStatsIntent  = "agent_stats"
	agentDetailIntent = "agent_detail"
	agentTableIntent  = "agent_table"
	agentListIntent   = "agent_list"
	agentChartIntent  = "agent_chart"
)

const (
	maxPresentColumns = 8
	maxPresentRows    = 50
	maxPresentItems   = 50
	maxPresentStats   = 8
	maxPresentDetail  = 16
	maxChartSeries    = 4
	maxChartPoints    = 60
)

type presentedStat struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

type presentedItem struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Badges   []string `json:"badges,omitempty"`
}

type presentedTable struct {
	Title   string           `json:"title,omitempty"`
	Columns []cards.Column   `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Total   int              `json:"total,omitempty"`
}

type presentedChart struct {
	Title  string              `json:"title,omitempty"`
	Kind   string              `json:"kind"`
	XKey   string              `json:"xKey"`
	Series []cards.ChartSeries `json:"series"`
	Data   []map[string]any    `json:"data"`
	Unit   string              `json:"unit,omitempty"`
}

type presentedList struct {
	Title string          `json:"title,omitempty"`
	Items []presentedItem `json:"items"`
	Total int             `json:"total,omitempty"`
}

type presentedFacts struct {
	Title string          `json:"title,omitempty"`
	Rows  []presentedStat `json:"rows"`
}

type presentedAnswer struct {
	Summary     string            `json:"summary"`
	Stats       *presentedFacts   `json:"stats,omitempty"`
	Chart       *presentedChart   `json:"chart,omitempty"`
	Table       *presentedTable   `json:"table,omitempty"`
	List        *presentedList    `json:"list,omitempty"`
	Detail      *presentedFacts   `json:"detail,omitempty"`
	Suggestions []suggestedAction `json:"suggestions,omitempty"`
}

func presentToolDef() genillm.Tool {
	str := map[string]any{"type": "string"}
	stat := map[string]any{"type": "object", "required": []string{"label", "value"},
		"properties": map[string]any{"label": str, "value": str, "hint": str}}
	viewTitle := map[string]any{"type": "string", "description": "short name of what this view shows (the report, metric, or person). Never \"ASI Genie\", \"Report\", or \"Table\"."}
	facts := map[string]any{"type": "object", "required": []string{"title", "rows"},
		"properties": map[string]any{"title": viewTitle, "rows": map[string]any{"type": "array", "items": stat}}}
	column := map[string]any{"type": "object", "required": []string{"key", "label"},
		"properties": map[string]any{"key": str, "label": str, "align": map[string]any{"type": "string", "enum": []any{"left", "right", "center"}}}}
	series := map[string]any{"type": "object", "required": []string{"key", "label"},
		"properties": map[string]any{"key": str, "label": str}}
	properties := map[string]any{
		"summary": map[string]any{"type": "string", "description": "one or two plain sentences"},
		"stats":   facts,
		"detail":  facts,
		"chart": map[string]any{"type": "object", "required": []string{"title", "kind", "xKey", "series", "data"},
			"properties": map[string]any{
				"title":  viewTitle,
				"kind":   map[string]any{"type": "string", "enum": []any{cards.ChartBar, cards.ChartLine, cards.ChartArea, cards.ChartPie}},
				"xKey":   map[string]any{"type": "string", "description": "data field holding the category or date label"},
				"series": map[string]any{"type": "array", "maxItems": maxChartSeries, "items": series},
				"data":   map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "one object per point: {xKey: label, seriesKey: number}"},
				"unit":   map[string]any{"type": "string", "description": "e.g. % or $"},
			}},
		"table": map[string]any{"type": "object", "required": []string{"title", "columns", "rows"},
			"properties": map[string]any{
				"title":   viewTitle,
				"columns": map[string]any{"type": "array", "maxItems": maxPresentColumns, "items": column},
				"rows":    map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "one object per row keyed by column key"},
				"total":   map[string]any{"type": "integer", "description": "full count when rows are a subset"},
			}},
		"list": map[string]any{"type": "object", "required": []string{"title", "items"},
			"properties": map[string]any{
				"title": viewTitle,
				"items": map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"title"},
					"properties": map[string]any{"title": str, "subtitle": str, "badges": map[string]any{"type": "array", "items": str}}}},
				"total": map[string]any{"type": "integer"},
			}},
	}
	if suggestionsEnabled() {
		properties["suggestions"] = map[string]any{
			"type":        "array",
			"maxItems":    maxSuggestions,
			"description": "2 or 3 next requests the user can send. Each message is performed when tapped.",
			"items": map[string]any{"type": "object", "required": []string{"label", "message"},
				"properties": map[string]any{
					"label":   map[string]any{"type": "string", "description": "short chip text"},
					"message": map[string]any{"type": "string", "description": "full request, copied names only, at most 160 characters"},
				}},
		}
	}
	return genillm.Tool{
		Name:        presentTool,
		Description: "Show the answer: a short summary plus optional stats, chart, table, list or detail views built only from the read results.",
		Parameters: map[string]any{
			"type":       "object",
			"required":   []string{"summary"},
			"properties": properties,
		},
	}
}

func (rc *reqCtx) present(a presentedAnswer) bool {
	summary := strings.TrimSpace(a.Summary)
	fillViewTitles(&a, subjectFromQuestion(rc.req.Message))
	built := presentCards(a)
	if summary == "" && len(built) == 0 {
		return false
	}
	if summary != "" {
		rc.text(summary)
	}
	for _, c := range built {
		rc.add(c)
	}
	if suggestionsEnabled() {
		rc.applySuggestions(a.Suggestions)
	}
	return true
}

var (
	viewTitleLead   = regexp.MustCompile(`(?i)^\s*(?:please\s+)?(?:can you\s+|could you\s+)?(?:show|display|list|get|give|pull|fetch|open)\s+(?:me\s+)?(?:the\s+)?`)
	viewTitleTail   = regexp.MustCompile(`(?i)\s+(?:in\s+(?:a\s+)?(?:table|chart|list|graph)|as\s+(?:a\s+)?(?:table|chart|list))(?:\s+please)?\s*[?.!]*\s*$`)
	viewTitleReport = regexp.MustCompile(`(?i)\s+report\s*$`)
)

func fillViewTitles(a *presentedAnswer, fallback string) {
	if isGenericViewTitle(fallback) {
		return
	}
	if a.Stats != nil {
		a.Stats.Title = titled(a.Stats.Title, fallback)
	}
	if a.Chart != nil {
		a.Chart.Title = titled(a.Chart.Title, fallback)
	}
	if a.Table != nil {
		a.Table.Title = titled(a.Table.Title, fallback)
	}
	if a.List != nil {
		a.List.Title = titled(a.List.Title, fallback)
	}
	if a.Detail != nil {
		a.Detail.Title = titled(a.Detail.Title, fallback)
	}
}

func titled(given, fallback string) string {
	if isGenericViewTitle(given) {
		return fallback
	}
	return strings.TrimSpace(given)
}

func isGenericViewTitle(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "", "asi genie", "genie", "report", "table", "chart", "stats", "summary", "data", "list", "detail", "overview":
		return true
	default:
		return false
	}
}

func subjectFromQuestion(msg string) string {
	s := strings.TrimSpace(msg)
	if s == "" {
		return ""
	}
	cleaned := viewTitleLead.ReplaceAllString(s, "")
	cleaned = viewTitleTail.ReplaceAllString(cleaned, "")
	cleaned = viewTitleReport.ReplaceAllString(cleaned, "")
	cleaned = strings.Trim(cleaned, " \t\"'?.!")
	if cleaned == "" {
		cleaned = strings.Trim(s, " \t\"'?.!")
	}
	return clipTitle(cleaned)
}

func clipTitle(s string) string {
	const maxTitleRunes = 72
	r := []rune(s)
	clipped := s
	if len(r) > maxTitleRunes {
		clipped = strings.TrimSpace(string(r[:maxTitleRunes])) + "…"
	}
	return capitalizeTitle(clipped)
}

func capitalizeTitle(s string) string {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return s
		}
	}
	rs := []rune(s)
	if len(rs) == 0 {
		return s
	}
	rs[0] = unicode.ToUpper(rs[0])
	return string(rs)
}

func presentCards(a presentedAnswer) []cards.Card {
	var out []cards.Card
	if c, ok := factsCard(a.Stats, agentStatsIntent, maxPresentStats); ok {
		out = append(out, c)
	}
	if c, ok := chartCard(a.Chart); ok {
		out = append(out, c)
	}
	if c, ok := tableCard(a.Table); ok {
		out = append(out, c)
	}
	if c, ok := listCard(a.List); ok {
		out = append(out, c)
	}
	if c, ok := factsCard(a.Detail, agentDetailIntent, maxPresentDetail); ok {
		out = append(out, c)
	}
	return out
}

func factsCard(f *presentedFacts, intent string, max int) (cards.Card, bool) {
	if f == nil {
		return cards.Card{}, false
	}
	var rows []cards.Field
	for _, r := range f.Rows {
		label, value := strings.TrimSpace(r.Label), strings.TrimSpace(r.Value)
		if label == "" || value == "" {
			continue
		}
		rows = append(rows, cards.Field{Label: label, Value: value, Hint: strings.TrimSpace(r.Hint)})
		if len(rows) == max {
			break
		}
	}
	if len(rows) == 0 {
		return cards.Card{}, false
	}
	return cards.Card{Type: cards.TypeData, Intent: intent, Title: strings.TrimSpace(f.Title), Rows: rows}, true
}

func tableCard(t *presentedTable) (cards.Card, bool) {
	if t == nil {
		return cards.Card{}, false
	}
	var cols []cards.Column
	seen := map[string]bool{}
	for _, c := range t.Columns {
		key := strings.TrimSpace(c.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		cols = append(cols, cards.Column{Key: key, Label: firstNonEmpty(strings.TrimSpace(c.Label), key), Align: c.Align})
		if len(cols) == maxPresentColumns {
			break
		}
	}
	if len(cols) == 0 {
		return cards.Card{}, false
	}
	rows := make([]map[string]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		row := map[string]string{}
		for _, c := range cols {
			row[c.Key] = cellText(r[c.Key])
		}
		rows = append(rows, row)
		if len(rows) == maxPresentRows {
			break
		}
	}
	if len(rows) == 0 {
		return cards.Card{}, false
	}
	return cards.Card{Type: cards.TypeTable, Intent: agentTableIntent, Title: strings.TrimSpace(t.Title),
		Table: &cards.TableData{Columns: cols, Rows: rows}, Total: max(t.Total, len(rows))}, true
}

func listCard(l *presentedList) (cards.Card, bool) {
	if l == nil {
		return cards.Card{}, false
	}
	var items []cards.ListItem
	for _, it := range l.Items {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			continue
		}
		items = append(items, cards.ListItem{Title: title, Subtitle: strings.TrimSpace(it.Subtitle), Badges: it.Badges})
		if len(items) == maxPresentItems {
			break
		}
	}
	if len(items) == 0 {
		return cards.Card{}, false
	}
	return cards.Card{Type: cards.TypeList, Intent: agentListIntent, Title: strings.TrimSpace(l.Title),
		Items: items, Total: max(l.Total, len(items))}, true
}

var listRequestRe = regexp.MustCompile(`(?i)\b(list|names?|all|every)\b`)

func messageWantsList(msg string) bool {
	return listRequestRe.MatchString(msg)
}

func hasListView(cs []cards.Card) bool {
	for _, c := range cs {
		if c.Type == cards.TypeList || c.Type == cards.TypeTable {
			return true
		}
	}
	return false
}

// The model sometimes answers "list all reports" with a count. The catalog names are in the read, so list them.
func (rc *reqCtx) ensureRequestedList(results []readResult) {
	if !messageWantsList(rc.req.Message) || hasListView(rc.resp.Cards) {
		return
	}
	if c, ok := reportCatalogListCard(results); ok {
		rc.add(c)
	}
}

func reportCatalogListCard(results []readResult) (cards.Card, bool) {
	for _, r := range results {
		if r.Tool != "reservation_get_reports_catalog" || r.Status != "executed" || len(r.Data) == 0 {
			continue
		}
		var raw any
		if err := json.Unmarshal(r.Data, &raw); err != nil {
			slog.Warn("report catalog result is not JSON; skipping list", "err", err)
			continue
		}
		list := findList(path(raw, "reports"))
		if list == nil {
			list = findList(raw)
		}
		items := make([]presentedItem, 0, len(list))
		for _, row := range list {
			name := str(row, "name", "Name")
			if name == "" {
				continue
			}
			item := presentedItem{Title: name, Subtitle: str(row, "groupName", "GroupName")}
			if dt := str(row, "dateSelectionType", "DateSelectionType"); dt != "" {
				item.Badges = []string{dt}
			}
			items = append(items, item)
		}
		return listCard(&presentedList{Title: "Reports", Items: items, Total: len(items)})
	}
	return cards.Card{}, false
}

// Drop points the model sent as text; the panel only plots numbers.
func chartCard(ch *presentedChart) (cards.Card, bool) {
	if ch == nil || strings.TrimSpace(ch.XKey) == "" {
		return cards.Card{}, false
	}
	kind := ch.Kind
	switch kind {
	case cards.ChartBar, cards.ChartLine, cards.ChartArea, cards.ChartPie:
	default:
		kind = cards.ChartBar
	}
	var series []cards.ChartSeries
	for _, s := range ch.Series {
		if strings.TrimSpace(s.Key) == "" || s.Key == ch.XKey {
			continue
		}
		series = append(series, cards.ChartSeries{Key: s.Key, Label: firstNonEmpty(strings.TrimSpace(s.Label), s.Key)})
		if len(series) == maxChartSeries || (kind == cards.ChartPie && len(series) == 1) {
			break
		}
	}
	if len(series) == 0 {
		return cards.Card{}, false
	}
	var data []map[string]any
	for _, p := range ch.Data {
		label := cellText(p[ch.XKey])
		if label == "" {
			continue
		}
		point := map[string]any{ch.XKey: label}
		hasValue := false
		for _, s := range series {
			if v, ok := chartNumber(p[s.Key]); ok {
				point[s.Key] = v
				hasValue = true
			}
		}
		if hasValue {
			data = append(data, point)
		}
		if len(data) == maxChartPoints {
			break
		}
	}
	if len(data) < 2 {
		return cards.Card{}, false
	}
	return cards.Card{Type: cards.TypeChart, Intent: agentChartIntent, Title: strings.TrimSpace(ch.Title),
		Chart: &cards.ChartData{Kind: kind, XKey: ch.XKey, Series: series, Data: data, Unit: strings.TrimSpace(ch.Unit)}}, true
}

func chartNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		s := strings.NewReplacer(",", "", "$", "", "%", "").Replace(strings.TrimSpace(n))
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	return 0, false
}

func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}
