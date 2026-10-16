package genie

import (
	"strings"
	"testing"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

func salesPayload(rows ...map[string]any) map[string]any {
	list := make([]any, 0, len(rows))
	for _, row := range rows {
		list = append(list, row)
	}
	return map[string]any{
		"success":     true,
		"total":       float64(1),
		"summaryRows": list,
	}
}

func TestRevenueFactsSingleDateOmitsMissingMetrics(t *testing.T) {
	t.Parallel()
	payload := salesPayload(
		map[string]any{"key": "totals", "dailyValues": map[string]any{"2026-10-14": 240.5, "2026-10-15": 10.0}},
		map[string]any{"key": "revenue_count", "dailyValues": map[string]any{"2026-10-14": 2.0}},
		map[string]any{"key": "adr", "dailyValues": map[string]any{}},
		map[string]any{"key": "occupancy_per_day", "dailyValues": map[string]any{"2026-10-15": 40.0}},
	)
	rows, empty := revenueFacts(payload, "2026-10-14", "2026-10-14")
	if empty {
		t.Fatal("expected revenue rows")
	}
	if len(rows) != 2 || rows[0].Label != "Total revenue" || rows[0].Value != "$240.50" || rows[1].Value != "2" {
		t.Fatalf("rows = %+v", rows)
	}
	for _, row := range rows {
		if row.Label == "Success" || row.Label == "Total" || row.Label == "ADR" || row.Label == "Occupancy" {
			t.Fatalf("unexpected row %+v", row)
		}
	}
}

func TestRevenueFactsExplicitRange(t *testing.T) {
	t.Parallel()
	payload := salesPayload(
		map[string]any{"key": "totals", "dailyValues": map[string]any{"2026-10-01": 100.0, "2026-10-02": 50.0}},
		map[string]any{"key": "revenue_count", "dailyValues": map[string]any{"2026-10-01": 1.0, "2026-10-02": 1.0}},
		map[string]any{"key": "adr", "dailyValues": map[string]any{"2026-10-01": 100.0, "2026-10-02": 50.0}},
		map[string]any{"key": "occupancy_per_day", "dailyValues": map[string]any{"2026-10-01": 10.0}},
	)
	rows, empty := revenueFacts(payload, "2026-10-01", "2026-10-02")
	if empty || len(rows) != 4 {
		t.Fatalf("rows = %+v empty=%v", rows, empty)
	}
	if rows[0].Value != "$150.00" || rows[1].Value != "2" || rows[2].Value != "$75.00" || rows[3].Value != "10.0%" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRevenueFactsAllZero(t *testing.T) {
	t.Parallel()
	payload := salesPayload(
		map[string]any{"key": "totals", "dailyValues": map[string]any{"2026-10-14": 0.0}},
		map[string]any{"key": "revenue_count", "dailyValues": map[string]any{"2026-10-14": 0.0}},
		map[string]any{"key": "adr", "dailyValues": map[string]any{}},
		map[string]any{"key": "occupancy_per_day", "dailyValues": map[string]any{}},
	)
	rows, empty := revenueFacts(payload, "2026-10-14", "2026-10-14")
	if !empty || len(rows) != 0 {
		t.Fatalf("rows = %+v empty=%v", rows, empty)
	}
}

func TestRevenueHandlerAsksBeforeCalling(t *testing.T) {
	t.Parallel()
	rc := &reqCtx{resp: &Response{}, bizDate: "2026-10-15"}
	err := hRevenue(rc, intents.Intent{ID: "revenue_query"}, Slots{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.resp.ToolCalls) != 0 {
		t.Fatalf("tool calls = %d", len(rc.resp.ToolCalls))
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeClarify {
		t.Fatalf("cards = %+v", rc.resp.Cards)
	}
	if strings.Contains(rc.resp.Text, "2026-10-01") {
		t.Fatalf("reply defaulted a window: %s", rc.resp.Text)
	}
}
