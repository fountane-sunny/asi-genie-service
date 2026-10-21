package genie

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

const nightAuditSettingsTool = "config_get_properties_id_night_audit_settings"

type reportCatalogItem struct {
	ID           int
	Name         string
	ComponentKey string
}

func (rc *reqCtx) loadReportCatalog() ([]reportCatalogItem, error) {
	raw, err := rc.get("reservation_get_reports_catalog", nil, nil)
	if err != nil {
		return nil, err
	}
	list := findList(path(raw, "reports"))
	if list == nil {
		list = findList(raw)
	}
	out := make([]reportCatalogItem, 0, len(list))
	for _, row := range list {
		idStr := str(row, "id", "ID")
		id, _ := strconv.Atoi(idStr)
		name := str(row, "name", "Name")
		key := str(row, "componentKey", "ComponentKey")
		if id == 0 || name == "" || key == "" {
			continue
		}
		out = append(out, reportCatalogItem{ID: id, Name: name, ComponentKey: key})
	}
	return out, nil
}

func normalizeReportPhrase(phrase string) string {
	p := strings.TrimSpace(phrase)
	p = strings.TrimSuffix(strings.ToLower(p), " report")
	return strings.TrimSpace(p)
}

func scoreReportPhrase(phrase string, item reportCatalogItem) int {
	if phrase == "" {
		return 0
	}
	n := normalizeReportPhrase(phrase)
	if n == "" {
		return 0
	}
	nameLower := strings.ToLower(item.Name)
	keyLower := strings.ToLower(item.ComponentKey)
	nameSlug := slug(strings.TrimSuffix(item.Name, " Report"))

	if strings.EqualFold(phrase, item.Name) || n == nameLower {
		return 1000
	}
	if n == keyLower || n == nameSlug {
		return 900
	}
	if strings.Contains(nameLower, n) {
		return 800
	}

	score := 0
	phraseSlug := slug(n)
	if phraseSlug == keyLower {
		score += 400
	}
	if strings.Contains(keyLower, phraseSlug) || strings.Contains(phraseSlug, keyLower) {
		score += 200
	}
	for _, u := range strings.Split(phraseSlug, "-") {
		if len(u) < 3 {
			continue
		}
		if strings.Contains(keyLower, u) || strings.Contains(nameSlug, u) {
			score += 10
		}
	}
	for _, w := range strings.Fields(n) {
		if len(w) < 3 {
			continue
		}
		if strings.Contains(nameLower, w) || strings.Contains(keyLower, w) {
			score += 5
		}
	}
	return score
}

const reportMatchMinScore = 15

type reportCatalogMatch struct {
	item  reportCatalogItem
	score int
}

func matchReportInCatalog(catalog []reportCatalogItem, phrase string) (*reportCatalogItem, []reportCatalogItem) {
	if len(catalog) == 0 || strings.TrimSpace(phrase) == "" {
		return nil, nil
	}
	ranked := make([]reportCatalogMatch, 0, len(catalog))
	for _, item := range catalog {
		score := scoreReportPhrase(phrase, item)
		if score >= reportMatchMinScore {
			ranked = append(ranked, reportCatalogMatch{item: item, score: score})
		}
	}
	if len(ranked) == 0 {
		return nil, nil
	}
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score > ranked[i].score ||
				(ranked[j].score == ranked[i].score && ranked[j].item.Name < ranked[i].item.Name) {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	top := ranked[0]
	if top.score < reportMatchMinScore {
		return nil, nil
	}
	var tied []reportCatalogItem
	for _, r := range ranked {
		if r.score == top.score {
			tied = append(tied, r.item)
		} else {
			break
		}
	}
	if len(tied) > 1 {
		return nil, tied
	}
	return &top.item, nil
}

// Unknown catalog ids are dropped so chat never prints a bare report number.
func (rc *reqCtx) nameNightAuditReports(raw json.RawMessage) json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		slog.Warn("genie night audit settings were not an object", "err", err, "propertyId", rc.pid)
		return raw
	}
	if intList(doc["selectedReportIds"]) == nil {
		return raw
	}
	catalog, err := rc.loadReportCatalog()
	if err != nil {
		slog.Warn("genie report catalog failed; night audit report ids left unresolved", "err", err, "propertyId", rc.pid)
		return raw
	}
	applyReportNames(doc, catalog, rc.pid)
	out, err := json.Marshal(doc)
	if err != nil {
		slog.Warn("genie night audit settings rewrite failed", "err", err, "propertyId", rc.pid)
		return raw
	}
	return out
}

func applyReportNames(doc map[string]any, catalog []reportCatalogItem, propertyID int) {
	ids := intList(doc["selectedReportIds"])
	if ids == nil {
		return
	}
	byID := make(map[int]string, len(catalog))
	for _, item := range catalog {
		byID[item.ID] = item.Name
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name := byID[id]
		if name == "" {
			slog.Warn("genie night audit report id has no catalog name", "reportId", id, "propertyId", propertyID)
			continue
		}
		names = append(names, name)
	}
	delete(doc, "selectedReportIds")
	doc["selectedReports"] = names
}

func intList(v any) []int {
	xs, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(xs))
	for _, x := range xs {
		switch n := x.(type) {
		case float64:
			out = append(out, int(n))
		case string:
			i, err := strconv.Atoi(strings.TrimSpace(n))
			if err != nil {
				slog.Warn("genie skipped a non-numeric report id", "err", err, "reportId", n)
				continue
			}
			out = append(out, i)
		}
	}
	return out
}

func reportDeepLinkParams(item reportCatalogItem, rc *reqCtx, s Slots) map[string]string {
	params := map[string]string{
		"reportId": strconv.Itoa(item.ID),
		"report":   item.ComponentKey,
	}
	if s.StartDate != "" {
		params["startDate"] = s.StartDate
		params["endDate"] = s.EndDate
	} else {
		params["date"] = rc.dayOr(s)
	}
	return params
}

func messageRequestsSpecificReport(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" || !strings.Contains(lower, "report") {
		return false
	}
	generic := []string{
		"open reports",
		"go to reports",
		"reports page",
		"report page",
		"show reports",
		"view reports",
		"which reports",
		"what reports",
	}
	for _, g := range generic {
		if lower == g {
			return false
		}
	}
	stripped := lower
	for _, noise := range []string{"give me", "link", "page", "of the", "the", "a", "an", "please", "open", "go to", "show", "view"} {
		stripped = strings.ReplaceAll(stripped, noise, " ")
	}
	stripped = strings.ReplaceAll(stripped, "reports", " ")
	stripped = strings.ReplaceAll(stripped, "report", " ")
	stripped = strings.Join(strings.Fields(stripped), " ")
	return len(stripped) >= 3
}

func (rc *reqCtx) handleReportDeepLink(it intents.Intent, s Slots, phrase string) error {
	catalog, err := rc.loadReportCatalog()
	if err != nil {
		return err
	}

	baseLink := cards.Link{Label: "Open reports", Route: p(rc.pid, "/reports")}
	phrase = strings.TrimSpace(phrase)

	if phrase == "" {
		params := map[string]string{"date": rc.dayOr(s)}
		baseLink.Params = params
		rc.add(navigateScreen(it.ID, "Reports", navigateActionGoThere, baseLink))
		rc.text("Open Reports and choose the one you need.")
		return nil
	}

	if len(catalog) == 0 {
		rc.add(navigateScreen(it.ID, "Reports", navigateActionGoThere, baseLink))
		rc.text("I couldn't load the report list. Open Reports to browse.")
		return nil
	}

	match, ambiguous := matchReportInCatalog(catalog, phrase)
	if len(ambiguous) > 1 {
		opts := make([]cards.Option, 0, len(ambiguous))
		for _, item := range ambiguous {
			opts = append(opts, cards.Option{
				Label:   item.Name,
				Value:   item.ComponentKey,
				Message: "Open " + item.Name,
			})
		}
		rc.add(cards.Clarify(it.ID, "Which report did you mean?", opts...))
		return nil
	}
	if match == nil {
		rc.add(navigateScreen(it.ID, "Reports", navigateActionGoThere, baseLink))
		rc.text("I couldn't find that report in your catalog. Open Reports to browse all reports.")
		return nil
	}

	params := reportDeepLinkParams(*match, rc, s)
	rc.add(navigateScreen(it.ID, match.Name, navigateActionOpenReport, cards.Link{
		Route:  p(rc.pid, "/reports"),
		Params: params,
	}))
	return nil
}
