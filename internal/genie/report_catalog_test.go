package genie

import "testing"

func testReportCatalog() []reportCatalogItem {
	return []reportCatalogItem{
		{ID: 42, Name: "Room Type Restriction Change Report", ComponentKey: "room-type-restriction-change"},
		{ID: 7, Name: "Credit Card Activity Report", ComponentKey: "credit-card-activity"},
		{ID: 3, Name: "Guest Arrival Report", ComponentKey: "guest-arrival"},
		{ID: 4, Name: "Guest Segmentation Report", ComponentKey: "guest-segmentation"},
		{ID: 10, Name: "Daily Summary Report", ComponentKey: "daily-summary"},
	}
}

func TestMatchReportInCatalog_exactAndSlug(t *testing.T) {
	t.Parallel()
	catalog := testReportCatalog()

	got, amb := matchReportInCatalog(catalog, "Room Type Restriction Change Report")
	if amb != nil || got == nil || got.ID != 42 {
		t.Fatalf("exact name: got=%v amb=%v", got, amb)
	}

	got, amb = matchReportInCatalog(catalog, "room-type-restriction-change")
	if amb != nil || got == nil || got.ComponentKey != "room-type-restriction-change" {
		t.Fatalf("component key: got=%v amb=%v", got, amb)
	}

	got, amb = matchReportInCatalog(catalog, "room restriction type report")
	if amb != nil || got == nil || got.ID != 42 {
		t.Fatalf("colloquial restriction: got=%v amb=%v", got, amb)
	}

	got, amb = matchReportInCatalog(catalog, "Guest Arrival")
	if amb != nil || got == nil || got.ID != 3 {
		t.Fatalf("guest arrival: got=%v amb=%v", got, amb)
	}
}

func TestMatchReportInCatalog_noMatch(t *testing.T) {
	t.Parallel()
	got, amb := matchReportInCatalog(testReportCatalog(), "xyz not a report")
	if got != nil || amb != nil {
		t.Fatalf("vague phrase should not match: got=%v amb=%v", got, amb)
	}
}

func TestMessageRequestsSpecificReport_userPhrase(t *testing.T) {
	t.Parallel()
	msg := "give me link of the room restriction type report page"
	if !messageRequestsSpecificReport(msg) {
		t.Fatal("expected specific report request")
	}
	if messageRequestsSpecificReport("open reports") {
		t.Fatal("generic browse should not be specific")
	}
}

func TestScoreReportPhrase_deterministic(t *testing.T) {
	t.Parallel()
	item := testReportCatalog()[0]
	a := scoreReportPhrase("restriction change", item)
	b := scoreReportPhrase("restriction change", item)
	if a != b || a < reportMatchMinScore {
		t.Fatalf("score=%d min=%d", a, reportMatchMinScore)
	}
}

func TestApplyReportNamesReplacesIDsWithCatalogNames(t *testing.T) {
	t.Parallel()
	doc := map[string]any{
		"nightAuditTime":    "00:00",
		"selectedReportIds": []any{float64(3), float64(10), float64(99)},
	}
	applyReportNames(doc, testReportCatalog(), 42)
	if _, ok := doc["selectedReportIds"]; ok {
		t.Fatal("selectedReportIds should be removed once names are known")
	}
	names, ok := doc["selectedReports"].([]string)
	if !ok || len(names) != 2 || names[0] != "Guest Arrival Report" || names[1] != "Daily Summary Report" {
		t.Fatalf("selectedReports = %#v", doc["selectedReports"])
	}
	if doc["nightAuditTime"] != "00:00" {
		t.Fatalf("other fields changed: %#v", doc)
	}
}
