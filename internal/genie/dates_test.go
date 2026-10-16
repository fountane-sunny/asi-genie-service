package genie

import (
	"strings"
	"testing"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
	"asi-genie-poc/toolregistry/generated"
)

func TestClassifierUsesWorkingDate(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	rc := &reqCtx{e: e, bizDate: "2026-10-01"}
	got := e.classifierSystem(rc)
	if !strings.Contains(got, "2026-10-01") || !strings.Contains(got, "Thursday") {
		t.Fatalf("classifier prompt missing working date:\n%s", got)
	}
}

func TestRevenueWindow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		slots    Slots
		from, to string
		ok       bool
	}{
		{name: "stated day", slots: Slots{Date: "2026-10-14"}, from: "2026-10-14", to: "2026-10-14", ok: true},
		{name: "explicit range", slots: Slots{StartDate: "2026-09-01", EndDate: "2026-09-15"}, from: "2026-09-01", to: "2026-09-15", ok: true},
		{name: "start only", slots: Slots{StartDate: "2026-09-01"}, from: "2026-09-01", to: "2026-09-01", ok: true},
		{name: "no date asks", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			from, to, ok := revenueWindow(tc.slots)
			if ok != tc.ok || from != tc.from || to != tc.to {
				t.Fatalf("window = %s → %s ok=%v, want %s → %s ok=%v", from, to, ok, tc.from, tc.to, tc.ok)
			}
		})
	}
}

func TestParseRouteIntentKeepsTopLevelDate(t *testing.T) {
	t.Parallel()
	id, _, slots, ok := parseRouteIntent([]byte(`{"date":"2026-10-14","intent":"revenue_query"}`))
	if !ok || id != "revenue_query" || slots.Date != "2026-10-14" {
		t.Fatalf("flat = %s %+v ok=%v", id, slots, ok)
	}
	_, _, nested, ok := parseRouteIntent([]byte(`{"intent":"revenue_query","date":"2026-10-01","slots":{"date":"2026-10-14"}}`))
	if !ok || nested.Date != "2026-10-14" {
		t.Fatalf("nested date = %+v", nested)
	}
}

func TestExplicitReportDates(t *testing.T) {
	t.Parallel()
	start, end, report, ok := explicitReportDates("/api/v1/reservations", Slots{}, "", "")
	if report || !ok || start != "" || end != "" {
		t.Fatalf("non-report = %q %q report=%v ok=%v", start, end, report, ok)
	}

	start, end, report, ok = explicitReportDates("/api/v1/reports/sales-and-revenue", Slots{Date: "2026-10-03"}, "2026-01-01", "2026-01-31")
	if !report || !ok || start != "2026-10-03" || end != "2026-10-03" {
		t.Fatalf("slots.date = %q %q ok=%v", start, end, ok)
	}

	start, end, report, ok = explicitReportDates("/api/v1/reports/deposit-ledger", Slots{}, "2026-10-01", "")
	if !report || ok || start != "2026-10-01" || end != "" {
		t.Fatalf("missing end = %q %q ok=%v", start, end, ok)
	}

	start, end, report, ok = explicitReportDates("/api/v1/reports/deposit-ledger", Slots{}, "2026-10-01", "2026-10-07")
	if !report || !ok || start != "2026-10-01" || end != "2026-10-07" {
		t.Fatalf("query range = %q %q ok=%v", start, end, ok)
	}
}

func TestReportGuardSkipsTheAPI(t *testing.T) {
	t.Parallel()
	rc := &reqCtx{resp: &Response{}, bizDate: "2026-10-16"}
	err := (&Engine{}).genericReadFromTool(rc, intents.Intent{ID: "view_report"}, Slots{}, cards.Link{},
		generated.ToolMeta{Name: "reservation_get_api_v1_reports_sales_and_revenue", Method: "GET", Path: "/api/v1/reports/sales-and-revenue", ReadOnly: true},
		[]byte(`{"query":{"propertyId":"12"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.resp.ToolCalls) != 0 {
		t.Fatalf("tool calls = %d", len(rc.resp.ToolCalls))
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeClarify {
		t.Fatalf("cards = %+v", rc.resp.Cards)
	}
}
