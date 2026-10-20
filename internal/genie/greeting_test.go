package genie

import (
	"strings"
	"testing"
)

func TestOutageCardOnlyWhenEveryReadIsDown(t *testing.T) {
	down := []readResult{{Status: "error", Error: "service_unavailable"}, {Status: "error", Error: "service_error"}}
	card, text, ok := outageCard(down)
	if !ok || card.Code != "service_unavailable" || text == "" {
		t.Fatalf("outage = %+v %q %v", card, text, ok)
	}
	if _, _, ok := outageCard([]readResult{{Status: "error", Error: "service_unavailable"}, {Status: "executed"}}); ok {
		t.Fatal("a successful read is not an outage")
	}
	if _, _, ok := outageCard([]readResult{{Status: "error", Error: "missing_parameter"}}); ok {
		t.Fatal("a bad parameter is not an outage")
	}
}

func TestGreetingContextAsksTheModelToWriteTheHello(t *testing.T) {
	hour := 9
	rc := &reqCtx{e: New(nil, logOnlyStore{}), req: Request{Message: "Hi", LocalHour: &hour}}
	got := rc.greetingContext()
	if !strings.Contains(got, "Good morning") || !strings.Contains(got, "name unknown") {
		t.Errorf("greeting context = %q", got)
	}
	rc.req.Message = "who arrives today?"
	if rc.greetingContext() != "" {
		t.Error("a question must not include greeting context")
	}
	sys := rc.plannerSystem()
	if strings.Contains(sys, "Good morning") {
		t.Error("planner prompt for a question must not carry a greeting")
	}
	rc.req.Message = "Hello"
	sys = rc.plannerSystem()
	if !strings.Contains(sys, "vary the wording every time") || !strings.Contains(sys, "Good morning") {
		t.Error("a greeting prompt must ask for a fresh hello and include the time of day")
	}
}

func TestGreetingHourPrefersClientClock(t *testing.T) {
	hour := 9
	rc := &reqCtx{req: Request{LocalHour: &hour}}
	if got := greetingHour(rc); got != 9 {
		t.Errorf("hour = %d, want the client hour", got)
	}
	bad := 30
	rc.req.LocalHour = &bad
	if got := greetingHour(rc); got < 0 || got > 23 {
		t.Errorf("out-of-range hour fell through as %d", got)
	}
}
