package genie

import (
	"testing"

	"asi-genie-poc/internal/intents"
	"asi-genie-poc/toolregistry/generated"
)

func TestIntentToolBindingsAreAllowedGeneratedReads(t *testing.T) {
	all := make(map[string]generated.ToolMeta)
	for _, tool := range generated.AllTools() {
		all[tool.Name] = tool
	}
	for intentID, names := range intentTools {
		for _, name := range names {
			tool, ok := all[name]
			if !ok {
				t.Fatalf("%s binds unknown generated tool %q", intentID, name)
			}
			if !IsAllowedReadTool(tool) {
				t.Fatalf("%s binds disallowed read tool %q", intentID, name)
			}
		}
	}
}

func TestOccupancyForecastAddsDetailsForRoomType(t *testing.T) {
	tools := toolsForIntent("occupancy_forecast", Slots{RoomType: "Deluxe"})
	if len(tools) != 2 || tools[1] != "reservation_get_inventory_forecasting_inventory_details" {
		t.Fatalf("unexpected room-type forecast tools: %v", tools)
	}
}

func TestTyposRouteAfterSpellingCorrection(t *testing.T) {
	cases := map[string]string{
		"list all the roroms": "list_rooms",
		"list all the roomw":  "list_rooms",
		"arivals tday":        "todays_arrivals",
	}
	for in, want := range cases {
		text, _ := intents.CorrectSpelling(in)
		if id, _, ok := ruleRoute(text); ok {
			if id != want {
				t.Fatalf("%q → %s, want %s", in, id, want)
			}
			continue
		}
		m := intents.KeywordMatch(text, 1)
		if len(m) == 0 || m[0].Intent.ID != want {
			t.Fatalf("%q (corrected %q) keyword-routed to %+v, want %s", in, text, m, want)
		}
	}
}

func TestRuleRoutePeopleAndRooms(t *testing.T) {
	cases := []struct {
		message string
		intent  string
		metric  string
		guest   string
	}{
		{message: "Who am I?", intent: "current_user"},
		{message: "List all the rooms", intent: "list_rooms"},
		{message: "List all the available rooms", intent: "list_rooms", metric: "available"},
		{message: "Who is Sunny Sahsi?", intent: "lookup_guest", guest: "Sunny Sahsi"},
		{message: "Who is arriving today?", intent: ""},
		{message: "How many rooms are still dirty?", intent: ""},
	}
	for _, tc := range cases {
		id, slots, ok := ruleRoute(tc.message)
		if tc.intent == "" {
			if ok {
				t.Fatalf("%q should stay with the catalogue router", tc.message)
			}
			continue
		}
		if !ok || id != tc.intent || slots.Metric != tc.metric || slots.GuestName != tc.guest {
			t.Fatalf("%q → id=%s ok=%v slots=%+v", tc.message, id, ok, slots)
		}
	}
}
