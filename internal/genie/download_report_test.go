package genie

import (
	"testing"

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
