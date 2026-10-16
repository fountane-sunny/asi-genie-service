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
		working  string
		from, to string
	}{
		{name: "stated day", slots: Slots{Date: "2026-10-03"}, working: "2026-10-16", from: "2026-10-03", to: "2026-10-03"},
		{name: "no date is month to date", working: "2026-10-16", from: "2026-10-01", to: "2026-10-16"},
		{name: "explicit range", slots: Slots{StartDate: "2026-09-01", EndDate: "2026-09-15"}, working: "2026-10-16", from: "2026-09-01", to: "2026-09-15"},
		{name: "start only", slots: Slots{StartDate: "2026-09-01"}, working: "2026-10-16", from: "2026-09-01", to: "2026-09-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			from, to := revenueWindow(tc.slots, tc.working)
			if from != tc.from || to != tc.to {
				t.Fatalf("window = %s → %s, want %s → %s", from, to, tc.from, tc.to)
			}
		})
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
