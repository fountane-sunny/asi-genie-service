package genie

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"regexp"
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
	today := rc.bizDate
	if len(today) != 10 {
		today = time.Now().Format("2006-01-02")
	}
	return fmt.Sprintf(`You are the intent router for ASI Genie (hotel PMS assistant), Phase 1: read-only answers, guides and navigation.
Always call %s exactly once. Never answer directly. Never compute numbers.
Today's business date is %s (%s). Resolve relative dates ("tonight", "this weekend", "Friday", "last Tuesday") to YYYY-MM-DD.
Do NOT invent dates: if availability is asked without any date, leave dates empty (the app will ask).
Read intent, not spelling: a misspelled PMS question routes to the closest capability (the message may already be spell-corrected).
Use "unsupported" for non-PMS questions (including "what is Python" and "wat is pythn"), and for writes, payments, refunds, or anything outside the list. These must never result in a property API call.
Hotel terms are in scope, not unsupported: "what is RevPAR", "what is ADR", "what is a folio" route to the closest metric or guide capability.
Use "smalltalk" only for greetings/thanks.
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
	// Snap misspelled words to catalogue vocabulary before any routing, so
	// "arivals tday" and "list the roroms" route like the corrected text.
	// Names and numbers are left alone (see intents.CorrectSpelling).
	routeText := rc.req.Message
	if corrected, changed := intents.CorrectSpelling(rc.req.Message); changed {
		routeText = corrected
		log.Printf("genie: spelling corrected %q -> %q", rc.req.Message, corrected)
	}
	// These questions are not in the catalogue. Without a rule the router
	// marks them unsupported or sends them to a navigate card.
	if id, ruleSlots, ok := ruleRoute(routeText); ok {
		slots = ruleSlots
		if it, found := intents.Get(id); found {
			return it, IntentInfo{ID: it.ID, ScenarioID: it.ScenarioID, ResponseType: it.ResponseType, Confidence: 1, Source: "rule"}, slots
		}
		return intents.Intent{ID: id, Module: "Genie"}, IntentInfo{ID: id, Handler: "dedicated", Confidence: 1, Source: "rule"}, slots
	}
	// Skip when the planner's LLM call already failed this request.
	if e.LLM != nil && !rc.llmFailed {
		var prior []genillm.Message
		if shouldSendConversationContext(rc.req.Message) {
			prior = rc.recentTurns()
		}
		msgs := classifierMessages(routeText, prior)
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
			intentID, confidence, parsed, ok := parseRouteIntent(tc.Arguments)
			if !ok {
				break
			}
			slots = parsed
			if intentID == intents.UnsupportedID || intentID == intents.SmallTalkID {
				return intents.Intent{ID: intentID, Module: "Genie"}, IntentInfo{ID: intentID, Handler: "fallback", Confidence: confidence, Source: "llm"}, slots
			}
			if it, ok := intents.Get(intentID); ok {
				return it, IntentInfo{ID: it.ID, ScenarioID: it.ScenarioID, ResponseType: it.ResponseType, Confidence: confidence, Source: "llm"}, slots
			}
		}
	}
	if m := intents.KeywordMatch(routeText, 1); len(m) > 0 && m[0].Score >= 0.2 {
		it := m[0].Intent
		return it, IntentInfo{ID: it.ID, ScenarioID: it.ScenarioID, ResponseType: it.ResponseType, Confidence: m[0].Score, Source: "keyword"}, slots
	}
	return intents.Intent{ID: intents.UnsupportedID, Module: "Genie"}, IntentInfo{ID: intents.UnsupportedID, Handler: "fallback", Source: "keyword"}, slots
}

var (
	roomWordRe     = regexp.MustCompile(`\brooms?\b`)
	roomListVerbRe = regexp.MustCompile(`\b(list|show|all|available|vacant|free)\b`)
	whoIsRe        = regexp.MustCompile(`(?i)^who(?:'s| is)\s+(.+)$`)
)

// ruleRoute covers questions the intent catalogue does not name, matching the
// mobile assistant: the signed-in user, a person on the property, and a room list.
func ruleRoute(message string) (string, Slots, bool) {
	lower := strings.ToLower(strings.TrimSpace(message))
	lower = strings.TrimRight(lower, " ?.!")
	if isCurrentUserQuestion(lower) {
		return "current_user", Slots{}, true
	}
	if isRoomListQuestion(lower) {
		var slots Slots
		if roomListWantsAvailable(lower) {
			slots.Metric = "available"
		}
		return "list_rooms", slots, true
	}
	if name, ok := whoIsPerson(message); ok {
		return "lookup_guest", Slots{GuestName: name}, true
	}
	return "", Slots{}, false
}

func isCurrentUserQuestion(lower string) bool {
	switch lower {
	case "who am i", "whoami", "my profile", "what's my name", "what is my name",
		"who is logged in", "who is signed in", "who am i logged in as":
		return true
	}
	return false
}

func isRoomListQuestion(lower string) bool {
	if !roomWordRe.MatchString(lower) {
		return false
	}
	if strings.Contains(lower, "room type") || strings.Contains(lower, "how many") {
		return false
	}
	return roomListVerbRe.MatchString(lower)
}

func roomListWantsAvailable(lower string) bool {
	return strings.Contains(lower, "available") || strings.Contains(lower, "vacant") || strings.Contains(lower, "free")
}

func whoIsPerson(message string) (string, bool) {
	m := whoIsRe.FindStringSubmatch(strings.TrimSpace(message))
	if len(m) < 2 {
		return "", false
	}
	name := strings.Trim(strings.TrimSpace(m[1]), `"'?.!`)
	lower := strings.ToLower(name)
	switch {
	case name == "", len(name) < 2:
		return "", false
	case strings.HasPrefix(lower, "arriv"), strings.HasPrefix(lower, "depart"),
		strings.HasPrefix(lower, "check"), strings.HasPrefix(lower, "due"),
		strings.HasPrefix(lower, "in room"), strings.HasPrefix(lower, "the contact"),
		strings.HasPrefix(lower, "logged"), strings.HasPrefix(lower, "signed"):
		return "", false
	}
	return name, true
}

// classifierMessages is the router transcript. Continuations keep the recent
// turns; a new question is only the current message.
func classifierMessages(message string, prior []genillm.Message) []genillm.Message {
	current := genillm.Message{Role: genillm.RoleUser, Content: message}
	if !shouldSendConversationContext(message) || len(prior) == 0 {
		return []genillm.Message{current}
	}
	out := make([]genillm.Message, len(prior)+1)
	copy(out, prior)
	out[len(prior)] = current
	return out
}

// recentTurns returns the last contextTurns user and assistant messages.
// Stored history is used when it includes the assistant replies. When it does
// not, the transcript sent with the request is used, so a follow-up such as
// "do it" still sees what the assistant just offered.
func (rc *reqCtx) recentTurns() []genillm.Message {
	stored := clipTurns(rc.turnsFromStore())
	client := clipTurns(rc.req.History)
	if len(stored) == 0 || (!hasAssistantTurn(stored) && hasAssistantTurn(client)) {
		return client
	}
	return stored
}

func (rc *reqCtx) turnsFromStore() []genillm.Message {
	if rc.req.SessionID == "" || rc.e == nil || rc.e.Store == nil {
		return nil
	}
	u := rc.currentUser()
	hist, err := rc.e.Store.History(rc.ctx, rc.req.SessionID, u.id, rc.pid, contextTurns)
	if err != nil {
		slog.Warn("genie history lookup failed", "err", err, "propertyId", rc.pid)
		return nil
	}
	out := make([]genillm.Message, 0, len(hist))
	for _, m := range hist {
		if (m.Role == genillm.RoleUser || m.Role == genillm.RoleAssistant) && strings.TrimSpace(m.Content) != "" {
			out = append(out, genillm.Message{Role: m.Role, Content: m.Content})
		}
	}
	return out
}

func clipTurns(in []genillm.Message) []genillm.Message {
	out := make([]genillm.Message, 0, len(in))
	for _, m := range in {
		content := clipTurn(m.Content)
		if (m.Role != genillm.RoleUser && m.Role != genillm.RoleAssistant) || content == "" {
			continue
		}
		out = append(out, genillm.Message{Role: m.Role, Content: content})
	}
	if len(out) > contextTurns {
		out = out[len(out)-contextTurns:]
	}
	return out
}

func hasAssistantTurn(msgs []genillm.Message) bool {
	for _, m := range msgs {
		if m.Role == genillm.RoleAssistant {
			return true
		}
	}
	return false
}

const maxTurnRunes = 500

func clipTurn(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxTurnRunes {
		return s
	}
	return string(r[:maxTurnRunes]) + "…"
}

// appendUserTurn adds the current user message without two user messages in a
// row, which the model API rejects.
func appendUserTurn(prior []genillm.Message, content string) []genillm.Message {
	if len(prior) > 0 && prior[len(prior)-1].Role == genillm.RoleUser {
		prior[len(prior)-1].Content += "\n" + content
		return prior
	}
	return append(prior, genillm.Message{Role: genillm.RoleUser, Content: content})
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
// A new question does not reuse that entity.
func (rc *reqCtx) applyContext(s *Slots) {
	if !shouldSendConversationContext(rc.req.Message) {
		return
	}
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

// parseRouteIntent reads a route_intent call. The model sometimes puts date
// (and the other slot fields) next to intent instead of under slots. Nested
// slots win when both are present. An empty date here is not filled in.
func parseRouteIntent(raw json.RawMessage) (intentID string, confidence float64, slots Slots, ok bool) {
	var head struct {
		Intent     string  `json:"intent"`
		Confidence float64 `json:"confidence"`
		Slots      Slots   `json:"slots"`
	}
	if json.Unmarshal(raw, &head) != nil || strings.TrimSpace(head.Intent) == "" {
		return "", 0, Slots{}, false
	}
	var flat Slots
	_ = json.Unmarshal(raw, &flat)
	return head.Intent, head.Confidence, mergeSlots(head.Slots, flat), true
}

func mergeSlots(primary, fallback Slots) Slots {
	if primary.Date == "" {
		primary.Date = fallback.Date
	}
	if primary.StartDate == "" {
		primary.StartDate = fallback.StartDate
	}
	if primary.EndDate == "" {
		primary.EndDate = fallback.EndDate
	}
	if primary.CompareStart == "" {
		primary.CompareStart = fallback.CompareStart
	}
	if primary.CompareEnd == "" {
		primary.CompareEnd = fallback.CompareEnd
	}
	if primary.GuestName == "" {
		primary.GuestName = fallback.GuestName
	}
	if primary.Confirmation == "" {
		primary.Confirmation = fallback.Confirmation
	}
	if primary.Room == "" {
		primary.Room = fallback.Room
	}
	if primary.RoomType == "" {
		primary.RoomType = fallback.RoomType
	}
	if primary.Report == "" {
		primary.Report = fallback.Report
	}
	if primary.Destination == "" {
		primary.Destination = fallback.Destination
	}
	if primary.Metric == "" {
		primary.Metric = fallback.Metric
	}
	if primary.Topic == "" {
		primary.Topic = fallback.Topic
	}
	if primary.Unsupported == "" {
		primary.Unsupported = fallback.Unsupported
	}
	if !primary.UsesContext {
		primary.UsesContext = fallback.UsesContext
	}
	return primary
}

// revenueWindow is the sales-and-revenue range. A single date is that day and
// is never widened. ok is false when the user gave no date, so the handler
// asks instead of calling the report with a default window.
func revenueWindow(s Slots) (from, to string, ok bool) {
	from, to = s.StartDate, s.EndDate
	if from == "" && to == "" {
		if s.Date == "" {
			return "", "", false
		}
		return s.Date, s.Date, true
	}
	if from == "" {
		from = to
	}
	if to == "" {
		to = from
	}
	return from, to, true
}

// explicitReportDates requires both ends of a report range. Slots win over the
// model's query. A single slots.date is that day. ok is false when the path is
// a report and either date is still missing.
func explicitReportDates(path string, slots Slots, queryStart, queryEnd string) (start, end string, report bool, ok bool) {
	if !strings.Contains(path, "/reports/") {
		return "", "", false, true
	}
	start, end = slots.StartDate, slots.EndDate
	if start == "" && end == "" && slots.Date != "" {
		start, end = slots.Date, slots.Date
	}
	if start == "" {
		start = queryStart
	}
	if end == "" {
		end = queryEnd
	}
	return start, end, true, start != "" && end != ""
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
