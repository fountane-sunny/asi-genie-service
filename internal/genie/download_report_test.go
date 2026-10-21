package genie

import (
	"strings"
	"testing"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

func TestMessageWantsFileExport(t *testing.T) {
	t.Parallel()
	if !messageWantsFileExport("download Guest Arrival Report in pdf") {
		t.Fatal("pdf download should export")
	}
	if !messageWantsFileExport("Export the arrivals report as Excel") {
		t.Fatal("excel export should export")
	}
	if messageWantsFileExport("show me the guest arrival report") {
		t.Fatal("opening a report is not a file export")
	}
}

func TestReportPhraseFromExportMessage(t *testing.T) {
	t.Parallel()
	phrase := reportPhraseFromExportMessage("download Guest Arrival Report in pdf")
	if phrase != "guest arrival report" {
		t.Fatalf("phrase=%q", phrase)
	}
	match, amb := matchReportInCatalog(testReportCatalog(), phrase)
	if amb != nil || match == nil || match.ComponentKey != "guest-arrival" {
		t.Fatalf("catalog match=%v amb=%v", match, amb)
	}
}

func TestParseStructuredDownload(t *testing.T) {
	t.Parallel()
	cmd, ok := parseStructuredDownload("Download Guest Arrival Report as PDF from 2026-10-01 to 2026-10-15")
	if !ok || cmd.name != "Guest Arrival Report" || cmd.format != "pdf" || cmd.start != "2026-10-01" || cmd.end != "2026-10-15" {
		t.Fatalf("cmd=%+v ok=%v", cmd, ok)
	}
	cmd, ok = parseStructuredDownload("Download Reservations from 2026-10-15 to 2026-10-15")
	if !ok || cmd.name != "Reservations" || cmd.format != "" || cmd.start != "2026-10-15" {
		t.Fatalf("no-format cmd=%+v ok=%v", cmd, ok)
	}
}

func TestApplyDownloadIntentOverridesNavigation(t *testing.T) {
	t.Parallel()
	it, info, slots := (&Engine{}).applyDownloadIntent(
		intents.Intent{ID: "navigate"},
		IntentInfo{ID: "navigate", Source: "llm"},
		Slots{},
		"download Guest Arrival Report in pdf",
	)
	if it.ID != "download_report" || info.Source != "rule" || slots.Report != "guest arrival report" {
		t.Fatalf("it=%s source=%s report=%q", it.ID, info.Source, slots.Report)
	}
}

func TestFindExportToolKnownReports(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  string
		path string
	}{
		{"guest-arrival", "/api/v1/reports/guest-arrival/export"},
		{"daily-reservation-sheet", "/api/v1/reports/daily-reservations/export"},
		{"guest-segmentation", "/api/v1/reports/segmentation/export"},
		{"tax-detail", "/api/v1/reports/tax-details/export"},
		{"room-type-rate-change", "/v1/reports/room-type-rate-change/export"},
		{"reservations", "/api/v1/reservations/export"},
		{"night-audit", "/api/v1/night-audit/export"},
	}
	for _, tc := range cases {
		tool, ok := findExportTool(tc.key)
		if !ok || tool.Path != tc.path {
			t.Fatalf("%s: ok=%v path=%s", tc.key, ok, tool.Path)
		}
		service, path := clientExportPath(tool)
		if service == "" || path == "" || path == tool.Path {
			t.Fatalf("%s client path service=%s path=%s", tc.key, service, path)
		}
	}
}

func TestExportFormatFollowUp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		msg    string
		format string
		ok     bool
	}{
		{"in pdf?", "pdf", true},
		{"as excel", "xlsx", true},
		{"pdf?", "pdf", true},
		{"what about pdf?", "pdf", true},
		{"can I get it in pdf?", "pdf", true},
		{"download Guest Arrival Report in pdf", "", false},
		{"what is a pdf?", "", false},
	}
	for _, tc := range cases {
		format, ok := exportFormatFollowUp(tc.msg)
		if ok != tc.ok || format != tc.format {
			t.Errorf("%q: format=%q ok=%v", tc.msg, format, ok)
		}
	}
}

func TestRouterOwnsFormatFollowUpAfterExport(t *testing.T) {
	t.Parallel()
	rc := &reqCtx{
		req:  Request{Message: "in pdf?"},
		resp: &Response{},
		sess: &session{LastIntent: "download_report"},
	}
	if !rc.routerOwnsTurn() {
		t.Fatal("a format follow-up after an export must stay off the planner")
	}
	rc.sess = &session{}
	if rc.routerOwnsTurn() {
		t.Fatal("a format question with no earlier file is not an export follow-up")
	}
}

func TestReservationsPDFFollowUpKeepsExcel(t *testing.T) {
	t.Parallel()
	rc := &reqCtx{
		e:    &Engine{},
		pid:  9,
		req:  Request{Message: "in pdf?", PropertyID: 9},
		resp: &Response{},
		sess: &session{
			LastIntent: "download_report",
			LastExport: &offeredExport{Title: "Reservations", Slug: "reservations", Start: "2026-10-10", End: "2026-10-16"},
		},
	}
	it, info, slots := rc.applyExportFormatFollowUp(intents.Intent{}, IntentInfo{}, Slots{})
	if it.ID != "download_report" || slots.Report != "Reservations" || slots.StartDate != "2026-10-10" || slots.EndDate != "2026-10-16" {
		t.Fatalf("it=%s slots=%+v info=%+v", it.ID, slots, info)
	}
	if err := hDownloadReport(rc, it, slots); err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(rc.resp.Text)
	for _, banned := range []string{"wrong", "incorrect", "no file"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("reply retracted the earlier export: %s", rc.resp.Text)
		}
	}
	if rc.resp.Text != "PDF isn't available for the Reservations export. The Excel file for 2026-10-10 to 2026-10-16 is still ready." {
		t.Fatalf("text=%q", rc.resp.Text)
	}
	if len(rc.resp.Cards) != 1 || len(rc.resp.Cards[0].Downloads) != 1 || rc.resp.Cards[0].Downloads[0].Label != "Download Excel" {
		t.Fatalf("cards=%+v", rc.resp.Cards)
	}
	if rc.resp.Cards[0].Downloads[0].Query["exportType"] != "xlsx" {
		t.Fatalf("exportType=%s", rc.resp.Cards[0].Downloads[0].Query["exportType"])
	}
}

func TestReportFormatFollowUpOffersBothFiles(t *testing.T) {
	t.Parallel()
	prior := offeredExport{Title: "Guest Arrival Report", Slug: "guest-arrival", Start: "2026-10-01", End: "2026-10-15"}
	cases := []struct {
		message string
		label   string
		export  string
	}{
		{"in pdf?", "Download PDF", "pdf"},
		{"in excel?", "Download Excel", "xlsx"},
	}
	for _, tc := range cases {
		rc := &reqCtx{
			e:    &Engine{},
			pid:  9,
			req:  Request{Message: tc.message, PropertyID: 9},
			resp: &Response{},
			sess: &session{LastIntent: "download_report", LastExport: &prior},
		}
		it, _ := intents.Get("download_report")
		it, _, slots := rc.applyExportFormatFollowUp(it, IntentInfo{}, Slots{})
		if err := hDownloadReport(rc, it, slots); err != nil {
			t.Fatalf("%s: %v", tc.message, err)
		}
		lower := strings.ToLower(rc.resp.Text)
		for _, banned := range []string{"wrong", "incorrect", "isn't available", "no file"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("%s retracted a supported format: %s", tc.message, rc.resp.Text)
			}
		}
		if len(rc.resp.Cards) != 1 || len(rc.resp.Cards[0].Downloads) != 1 {
			t.Fatalf("%s cards=%+v", tc.message, rc.resp.Cards)
		}
		download := rc.resp.Cards[0].Downloads[0]
		if download.Label != tc.label || download.Query["exportType"] != tc.export || download.Path != "/reports/guest-arrival/export" {
			t.Fatalf("%s download=%+v", tc.message, download)
		}
	}
}

func TestReportExportOffersPDFAndExcel(t *testing.T) {
	t.Parallel()
	rc := &reqCtx{e: &Engine{}, pid: 9, req: Request{Message: "download Guest Arrival Report"}, resp: &Response{}, sess: &session{}}
	it, _ := intents.Get("download_report")
	err := rc.offerExport(it, "Guest Arrival Report", "guest-arrival", "", false, Slots{StartDate: "2026-10-01", EndDate: "2026-10-15"}, cards.Link{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.resp.Cards) != 1 || len(rc.resp.Cards[0].Downloads) != 2 {
		t.Fatalf("cards=%+v", rc.resp.Cards)
	}
	got := rc.resp.Cards[0].Downloads[0].Query["exportType"] + "," + rc.resp.Cards[0].Downloads[1].Query["exportType"]
	if got != "pdf,xlsx" {
		t.Fatalf("formats=%s", got)
	}
}

func TestExportDownloadQuery(t *testing.T) {
	t.Parallel()
	tool, ok := findExportTool("guest-arrival")
	if !ok {
		t.Fatal("missing guest arrival export")
	}
	d, ok := exportDownload(tool, "Guest Arrival Report", "pdf", "9", "2026-10-01", "2026-10-15")
	if !ok || d.Label != "Download PDF" || d.Query["exportType"] != "pdf" || d.Query["startDate"] != "2026-10-01" || d.Path != "/reports/guest-arrival/export" {
		t.Fatalf("download=%+v ok=%v", d, ok)
	}
}
