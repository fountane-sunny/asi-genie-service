package genie

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
	"asi-genie-poc/toolregistry/generated"
)

// reportExportSlugs maps a catalog component key to the path slug of its export API
// when the two names differ.
var reportExportSlugs = map[string][]string{
	"daily-reservation-sheet": {"daily-reservations"},
	"guest-segmentation":      {"segmentation"},
	"tax-detail":              {"tax-details"},
	"no-show-cancel-revenue":  {"noshow-cancellation-revenue"},
	"reservation-detail":      {"reservation-details"},
	"guest-stay-summary":      {"stay-summary"},
	"room-status-change":      {"room-status-changes"},
}

// listExports are screens that export PDF/Excel but are not rows in the report catalog.
var listExports = []struct {
	title string
	hints []string
	slug  string
}{
	{title: "Reservations", hints: []string{"reservation list", "reservations", "booking list"}, slug: "reservations"},
	{title: "Night audit", hints: []string{"night audit"}, slug: "night-audit"},
}

var (
	structuredDownloadAs = regexp.MustCompile(`(?i)^download\s+(.+?)\s+as\s+(pdf|excel|xlsx|csv)\s+from\s+(\d{4}-\d{2}-\d{2})\s+to\s+(\d{4}-\d{2}-\d{2})\s*$`)
	structuredDownload   = regexp.MustCompile(`(?i)^download\s+(.+?)\s+from\s+(\d{4}-\d{2}-\d{2})\s+to\s+(\d{4}-\d{2}-\d{2})\s*$`)
	isoDate              = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
)

type downloadCommand struct {
	name   string
	format string
	start  string
	end    string
}

// applyDownloadIntent sends "download/export … pdf|excel" to download_report
// instead of a navigation card. A duration chip is a full command, so it does
// not depend on the previous turn's intent.
func (e *Engine) applyDownloadIntent(it intents.Intent, info IntentInfo, slots Slots, message string) (intents.Intent, IntentInfo, Slots) {
	// A folio/registration/other-charges document is a reservation action
	// (download_folio / email_folio), not a report export: leave classify's
	// choice (or the follow-up resolver's) alone.
	if messageWantsFolioAction(message) {
		return it, info, slots
	}
	cmd, structured := parseStructuredDownload(message)
	if !structured && !messageWantsFileExport(message) {
		return it, info, slots
	}
	next, ok := intents.Get("download_report")
	if !ok {
		return it, info, slots
	}
	if structured {
		slots.Report = cmd.name
		slots.StartDate = cmd.start
		slots.EndDate = cmd.end
		if slots.StartDate != "" && slots.EndDate == "" {
			slots.EndDate = slots.StartDate
		}
	} else if strings.TrimSpace(slots.Report) == "" {
		slots.Report = reportPhraseFromExportMessage(message)
	}
	info = IntentInfo{
		ID:           next.ID,
		ScenarioID:   next.ScenarioID,
		ResponseType: next.ResponseType,
		Handler:      "dedicated",
		Source:       "rule",
		Confidence:   1,
		Slots:        slots,
	}
	return next, info, slots
}

func hDownloadReport(rc *reqCtx, it intents.Intent, s Slots) error {
	format, formatKnown := exportFormat(rc.req.Message)
	phrase := strings.TrimSpace(s.Report)
	if phrase == "" && rc.sess.LastEntity != nil && rc.sess.LastEntity.Kind == "report" {
		phrase = rc.sess.LastEntity.Label
	}
	if phrase == "" {
		phrase = reportPhraseFromExportMessage(rc.req.Message)
	}
	if list, ok := matchListExport(phrase); ok && !strings.Contains(strings.ToLower(phrase), "report") {
		return rc.offerExport(it, list.title, list.slug, format, formatKnown, s, listOpenLink(rc.pid, list.slug))
	}

	catalog, err := rc.loadReportCatalog()
	if err != nil {
		return err
	}
	match, ambiguous := matchReportInCatalog(catalog, phrase)
	if len(ambiguous) > 1 {
		opts := make([]cards.Option, 0, len(ambiguous))
		for _, item := range ambiguous {
			opts = append(opts, cards.Option{
				Label:   item.Name,
				Value:   item.ComponentKey,
				Message: downloadAsk(item.Name, format, "", ""),
			})
		}
		rc.add(cards.Clarify(it.ID, "Which report should I download?", opts...))
		return nil
	}
	if match == nil {
		rc.add(navigateScreen(it.ID, "Reports", navigateActionGoThere, cards.Link{
			Route: p(rc.pid, "/reports"),
		}))
		rc.text("I couldn't find that report. Open Reports and use Export on the one you need.")
		return nil
	}
	rc.remember(Entity{Kind: "report", ID: match.ComponentKey, Label: match.Name})
	open := cards.Link{
		Route:  p(rc.pid, "/reports"),
		Params: reportDeepLinkParams(*match, rc, s),
	}
	return rc.offerExport(it, match.Name, match.ComponentKey, format, formatKnown, s, open)
}

func (rc *reqCtx) offerExport(it intents.Intent, title, slug, format string, formatKnown bool, s Slots, open cards.Link) error {
	tool, ok := findExportTool(slug)
	if !ok {
		if open.Route == "" {
			open = cards.Link{Route: p(rc.pid, "/reports")}
		}
		rc.add(navigateScreen(it.ID, title, navigateActionOpenReport, open))
		rc.text("I can't download that from here. Open it and use Export on that screen.")
		return nil
	}
	start, end, datesOK := exportWindow(s)
	if !datesOK {
		rc.askExportDuration(it, title, format)
		return nil
	}
	formats := []string{format}
	if !formatKnown {
		formats = []string{"pdf", "xlsx"}
	}
	downloads := make([]cards.Download, 0, len(formats))
	for _, f := range formats {
		if d, ok := exportDownload(tool, title, f, rc.pidStr(), start, end); ok {
			downloads = append(downloads, d)
		}
	}
	if len(downloads) == 0 {
		return fmt.Errorf("export for %s has no download", title)
	}
	card := cards.Card{
		Type:      cards.TypeData,
		Intent:    it.ID,
		Title:     title,
		Subtitle:  exportSubtitle(start, end, formats),
		Message:   "Here's your export.",
		Downloads: downloads,
	}
	if open.Route != "" {
		open.Label = navigateActionOpenReport
		card.Link = &open
	}
	rc.add(card)
	rc.text("Here's your " + title + " export for " + revenueDateLabel(start, end) + ".")
	return nil
}

func (rc *reqCtx) askExportDuration(it intents.Intent, title, format string) {
	biz := rc.bizDate
	if len(biz) != 10 {
		biz = "today"
	}
	var opts []cards.Option
	if len(biz) == 10 {
		monthStart := biz[:8] + "01"
		last7 := addDays(biz, -6)
		opts = []cards.Option{
			{Label: "That day (" + biz + ")", Value: biz, Message: downloadAsk(title, format, biz, biz)},
			{Label: "Last 7 days (" + last7 + " to " + biz + ")", Value: last7 + ".." + biz, Message: downloadAsk(title, format, last7, biz)},
			{Label: "Month to date (" + monthStart + " to " + biz + ")", Value: monthStart + ".." + biz, Message: downloadAsk(title, format, monthStart, biz)},
		}
	}
	rc.add(cards.Clarify(it.ID, "Which dates should this download cover?", opts...))
}

func downloadAsk(title, format, start, end string) string {
	msg := "Download " + strings.TrimSpace(title)
	if format != "" {
		msg += " as " + formatWord(format)
	}
	if start != "" && end != "" {
		msg += " from " + start + " to " + end
	}
	return msg
}

func formatWord(format string) string {
	switch format {
	case "pdf":
		return "PDF"
	case "csv":
		return "CSV"
	default:
		return "Excel"
	}
}

func exportSubtitle(start, end string, formats []string) string {
	labels := make([]string, 0, len(formats))
	for _, f := range formats {
		labels = append(labels, formatWord(f))
	}
	return strings.Join(labels, " or ") + " · " + revenueDateLabel(start, end)
}

func exportWindow(s Slots) (start, end string, ok bool) {
	start, end = s.StartDate, s.EndDate
	if start == "" && end == "" && s.Date != "" {
		start, end = s.Date, s.Date
	}
	if start == "" || end == "" {
		return "", "", false
	}
	return start, end, true
}

func exportDownload(tool generated.ToolMeta, title, format, propertyID, start, end string) (cards.Download, bool) {
	service, path := clientExportPath(tool)
	if service == "" || path == "" {
		return cards.Download{}, false
	}
	ext := format
	if format == "xlsx" {
		ext = "xlsx"
	}
	return cards.Download{
		Label:    "Download " + formatWord(format),
		FileName: title + "." + ext,
		Service:  service,
		Path:     path,
		Query: map[string]string{
			"propertyId":     propertyID,
			"startDate":      start,
			"endDate":        end,
			"date":           start,
			"exportType":     format,
			"filtersApplied": "false",
		},
	}, true
}

// clientExportPath strips the service prefix so the browser can append the path
// to RESERVATION_SERVER_URL (/api/v1) or SERVER_URL (/v1).
func clientExportPath(tool generated.ToolMeta) (service, path string) {
	p := tool.Path
	switch tool.Service {
	case "reservation":
		p = strings.TrimPrefix(p, "/api/v1")
	case "config":
		p = strings.TrimPrefix(p, "/v1")
	default:
		return "", ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return tool.Service, p
}

func findExportTool(componentKey string) (generated.ToolMeta, bool) {
	slugs := reportExportSlugs[componentKey]
	if len(slugs) == 0 {
		slugs = []string{componentKey}
	}
	for _, slug := range slugs {
		needle := "/" + slug + "/export"
		for _, t := range generated.AllTools() {
			if t.Method != http.MethodGet || !t.ReadOnly {
				continue
			}
			if strings.Contains(t.Path, needle) || strings.HasSuffix(t.Path, "/"+slug+"/export") {
				return t, true
			}
		}
	}
	return generated.ToolMeta{}, false
}

func listOpenLink(pid int, slug string) cards.Link {
	switch slug {
	case "reservations":
		return cards.Link{Route: p(pid, "/reservation")}
	case "night-audit":
		return cards.Link{Route: p(pid, "/night-audit")}
	default:
		return cards.Link{}
	}
}

func matchListExport(phrase string) (struct {
	title string
	slug  string
}, bool) {
	n := strings.ToLower(strings.TrimSpace(phrase))
	var out struct {
		title string
		slug  string
	}
	if n == "" {
		return out, false
	}
	for _, item := range listExports {
		for _, hint := range item.hints {
			if n == hint || strings.Contains(n, hint) {
				out.title = item.title
				out.slug = item.slug
				return out, true
			}
		}
	}
	return out, false
}

func messageWantsFileExport(msg string) bool {
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "download") && !strings.Contains(lower, "export") {
		return false
	}
	return strings.Contains(lower, "pdf") ||
		strings.Contains(lower, "excel") ||
		strings.Contains(lower, "xlsx") ||
		strings.Contains(lower, "csv") ||
		strings.Contains(lower, "report") ||
		strings.Contains(lower, "list")
}

func exportFormat(msg string) (string, bool) {
	lower := strings.ToLower(msg)
	bestAt := -1
	format := ""
	for _, pair := range []struct{ needle, format string }{
		{"pdf", "pdf"},
		{"excel", "xlsx"},
		{"xlsx", "xlsx"},
		{"csv", "csv"},
	} {
		if at := strings.LastIndex(lower, pair.needle); at > bestAt {
			bestAt = at
			format = pair.format
		}
	}
	if bestAt < 0 {
		return "", false
	}
	return format, true
}

func parseStructuredDownload(message string) (downloadCommand, bool) {
	if m := structuredDownloadAs.FindStringSubmatch(strings.TrimSpace(message)); m != nil {
		format, _ := exportFormat(m[2])
		return downloadCommand{name: strings.TrimSpace(m[1]), format: format, start: m[3], end: m[4]}, true
	}
	if m := structuredDownload.FindStringSubmatch(strings.TrimSpace(message)); m != nil {
		return downloadCommand{name: strings.TrimSpace(m[1]), start: m[2], end: m[3]}, true
	}
	return downloadCommand{}, false
}

func reportPhraseFromExportMessage(msg string) string {
	if cmd, ok := parseStructuredDownload(msg); ok {
		return cmd.name
	}
	s := strings.ToLower(msg)
	for _, w := range []string{
		"download", "export", "as pdf", "as excel", "as xlsx", "as csv",
		"in pdf", "in excel", "in xlsx", "in csv",
		"pdf", "excel", "xlsx", "csv", "please",
	} {
		s = strings.ReplaceAll(s, w, " ")
	}
	s = isoDate.ReplaceAllString(s, " ")
	for _, w := range []string{" from ", " to ", " for ", " the ", " a ", " an "} {
		s = strings.ReplaceAll(s, w, " ")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}
