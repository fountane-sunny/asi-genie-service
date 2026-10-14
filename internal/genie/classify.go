package genie

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"asi-genie-poc/internal/intents"
	genillm "asi-genie-poc/llm"
)

// Slots are the parameters extracted from the message (never computed values).
type Slots struct {
	Date         string `json:"date,omitempty"`      // YYYY-MM-DD
	StartDate    string `json:"startDate,omitempty"` // YYYY-MM-DD
	EndDate      string `json:"endDate,omitempty"`   // YYYY-MM-DD
	CompareStart string `json:"compareStartDate,omitempty"`
	CompareEnd   string `json:"compareEndDate,omitempty"`
	GuestName    string `json:"guestName,omitempty"`
	Confirmation string `json:"confirmationNumber,omitempty"`
	Room         string `json:"roomNumber,omitempty"`
	RoomType     string `json:"roomType,omitempty"`
	Report       string `json:"reportName,omitempty"`
	Destination  string `json:"destination,omitempty"` // screen name for navigate
	Metric       string `json:"metric,omitempty"`      // adr | occupancy | revpar | revenue
	Topic        string `json:"topic,omitempty"`       // how-to topic for guide answers
	UsesContext  bool   `json:"usesContext,omitempty"` // message refers to an earlier entity ("her", "that booking")
	Unsupported  string `json:"unsupportedWhat,omitempty"`
}

const routeTool = "route_intent"

func (e *Engine) intentCatalogPrompt() string {
	var b strings.Builder
	for _, it := range intents.All() {
		ex := it.Examples
		if len(ex) > 3 {
			ex = ex[:3]
		}
		fmt.Fprintf(&b, "- %s [%s; %s]: %s\n", it.ID, it.Module, it.ResponseType, strings.Join(ex, " | "))
	}
	return b.String()
}

func routeToolDef() genillm.Tool {
	ids := []any{intents.UnsupportedID, intents.SmallTalkID}
	for _, it := range intents.All() {
		ids = append(ids, it.ID)
	}
	s := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	return genillm.Tool{
		Name:        routeTool,
		Description: "Route the user's message to exactly one Genie capability and extract its slots.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"intent"},
			"properties": map[string]any{
				"intent":     map[string]any{"type": "string", "enum": ids, "description": "capability id"},
				"confidence": map[string]any{"type": "number", "description": "0..1"},
				"slots": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"date":               s("single date YYYY-MM-DD, only if the user stated or implied one"),
						"startDate":          s("range start YYYY-MM-DD"),
						"endDate":            s("range end YYYY-MM-DD"),
						"compareStartDate":   s("comparison period start YYYY-MM-DD"),
						"compareEndDate":     s("comparison period end YYYY-MM-DD"),
						"guestName":          s("guest or group name as typed"),
						"confirmationNumber": s("reservation/confirmation number"),
						"roomNumber":         s("room number"),
						"roomType":           s("room type name"),
						"reportName":         s("report name, e.g. Daily Summary"),
						"destination":        s("screen to open, e.g. night audit, guest ledger, new reservation"),
						"metric":             s("adr | occupancy | revpar | revenue"),
						"topic":              s("how-to topic for guide questions"),
						"usesContext":        map[string]any{"type": "boolean", "description": "true if the message refers to an earlier guest/reservation/room (her, that booking, same room)"},
						"unsupportedWhat":    s("for intent=unsupported: short verb phrase of what was asked, e.g. 'take F&B orders'"),
					},
				},
			},
		},
	}
}

func (e *Engine) classifierSystem(rc *reqCtx) string {
	today := rc.req.BusinessDate
	if today == "" {
		today = time.Now().Format("2006-01-02")
	}
	return fmt.Sprintf(`You are the intent router for ASI Genie (hotel PMS assistant), Phase 1: read-only answers, guides and navigation.
Always call %s exactly once. Never answer directly. Never compute numbers.
Today's business date is %s (%s). Resolve relative dates ("tonight", "this weekend", "Friday", "last Tuesday") to YYYY-MM-DD.
Do NOT invent dates: if availability is asked without any date, leave dates empty (the app will ask).
Requests to create/change/cancel/charge something map to the matching read/readiness capability if one exists (e.g. "check in Smith" -> quick_checkin, "mark 201 clean" -> update_room_status, "book a room" -> create_reservation); otherwise use "unsupported".
Use "unsupported" for anything outside the list (F&B orders, weather, etc.) and "smalltalk" for greetings/thanks.
Capabilities (id [module; response type]: examples):
%s`, routeTool, today, mustWeekday(today), e.intentCatalogPrompt())
}

func mustWeekday(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return ""
	}
	return t.Weekday().String()
}

// classify uses the LLM as a router (function call over the intent list),
// falling back to deterministic keyword matching when the LLM is unavailable.
func (e *Engine) classify(rc *reqCtx) (intents.Intent, IntentInfo, Slots) {
	var slots Slots
	if e.LLM != nil {
		msgs := rc.recentTurns()
		msgs = append(msgs, genillm.Message{Role: genillm.RoleUser, Content: rc.req.Message})
		ctx, cancel := rc.llmCtx()
		res, err := e.LLM.SendMessage(ctx, e.classifierSystem(rc), msgs, []genillm.Tool{routeToolDef()})
		cancel()
		if err != nil {
			rc.llmFailed = true
			log.Printf("genie: classifier llm error: %v (falling back to keywords)", err)
		}
		for _, tc := range res.ToolCalls {
			if tc.Name != routeTool {
				continue
			}
			var out struct {
				Intent     string  `json:"intent"`
				Confidence float64 `json:"confidence"`
				Slots      Slots   `json:"slots"`
			}
			if json.Unmarshal(tc.Arguments, &out) != nil {
				break
			}
			slots = out.Slots
			if out.Intent == intents.UnsupportedID || out.Intent == intents.SmallTalkID {
				return intents.Intent{ID: out.Intent, Module: "Genie"}, IntentInfo{ID: out.Intent, Handler: "fallback", Confidence: out.Confidence, Source: "llm"}, slots
			}
			if it, ok := intents.Get(out.Intent); ok {
				return it, IntentInfo{ID: it.ID, ScenarioID: it.ScenarioID, ResponseType: it.ResponseType, Confidence: out.Confidence, Source: "llm"}, slots
			}
		}
	}
	if m := intents.KeywordMatch(rc.req.Message, 1); len(m) > 0 && m[0].Score >= 0.2 {
		it := m[0].Intent
		return it, IntentInfo{ID: it.ID, ScenarioID: it.ScenarioID, ResponseType: it.ResponseType, Confidence: m[0].Score, Source: "keyword"}, slots
	}
	return intents.Intent{ID: intents.UnsupportedID, Module: "Genie"}, IntentInfo{ID: intents.UnsupportedID, Handler: "fallback", Source: "keyword"}, slots
}

// recentTurns returns the last contextTurns stored user/assistant messages.
func (rc *reqCtx) recentTurns() []genillm.Message {
	var out []genillm.Message
	if rc.req.SessionID != "" {
		hist, err := rc.e.Store.History(rc.ctx, rc.req.SessionID, contextTurns)
		if err == nil {
			for _, m := range hist {
				if (m.Role == genillm.RoleUser || m.Role == genillm.RoleAssistant) && m.Content != "" {
					out = append(out, genillm.Message{Role: m.Role, Content: m.Content})
				}
			}
		}
	}
	if len(out) == 0 {
		for _, m := range rc.req.History {
			if (m.Role == genillm.RoleUser || m.Role == genillm.RoleAssistant) && m.Content != "" {
				out = append(out, genillm.Message{Role: m.Role, Content: m.Content})
			}
		}
		if len(out) > contextTurns {
			out = out[len(out)-contextTurns:]
		}
	}
	return out
}

func validDate(s string) string {
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return s
	}
	return ""
}

// resolveSlotDates validates dates; a single date fills an empty range.
func (rc *reqCtx) resolveSlotDates(s *Slots) {
	s.Date, s.StartDate, s.EndDate = validDate(s.Date), validDate(s.StartDate), validDate(s.EndDate)
	s.CompareStart, s.CompareEnd = validDate(s.CompareStart), validDate(s.CompareEnd)
	if s.StartDate != "" && s.EndDate == "" {
		s.EndDate = s.StartDate
	}
	if s.EndDate != "" && s.StartDate == "" {
		s.StartDate = s.EndDate
	}
	if s.EndDate != "" && s.StartDate > s.EndDate {
		s.StartDate, s.EndDate = s.EndDate, s.StartDate
	}
}

// applyContext fills entity slots from the previous turn (G-097).
func (rc *reqCtx) applyContext(s *Slots) {
	ent := rc.sess.LastEntity
	if ent == nil || s.GuestName != "" || s.Confirmation != "" || s.Room != "" {
		return
	}
	if !s.UsesContext {
		return
	}
	switch ent.Kind {
	case "reservation":
		s.Confirmation = ent.ID
	case "guest":
		s.GuestName = ent.Label
	case "room":
		s.Room = ent.ID
	}
}

// dayOr returns s.Date, else the business date.
func (rc *reqCtx) dayOr(s Slots) string {
	if s.Date != "" {
		return s.Date
	}
	if s.StartDate != "" {
		return s.StartDate
	}
	return rc.bizDate
}

func addDays(d string, n int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}
