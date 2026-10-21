package genie

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"asi-genie-poc/internal/cards"
	genillm "asi-genie-poc/llm"
	"asi-genie-poc/toolregistry/generated"
	"golang.org/x/sync/errgroup"
)

type plannerTool struct {
	Name  string
	Label string
	Use   string
}

var plannerIndex = []plannerTool{
	{"config_get_properties_id_basic_details", "Checking property details", "property name, working date, timezone, currency"},
	{"auth_get_current_user", "Checking your profile", "the signed-in user (\"who am I\")"},
	{"config_get_properties_id_users", "Checking users", "staff on this property; query searchKey=name or email"},
	{"config_get_roles", "Checking roles", "roles and privileges"},
	{"config_get_room_space_statuses", "Checking rooms", "each room and the status it is in right now, plus type and floor; query searchKey=room number, pageSize. Not the list of status names"},
	{"config_get_rooms", "Checking rooms", "room master list (number, room type). Not room statuses"},
	{"config_get_room_types", "Checking room types", "room types (id, code, name, occupancy)"},
	{"config_get_room_statuses", "Checking room statuses", "the room status catalog (name, shortName, color, description, isPreset). Use this when the user asks which statuses exist, their colors, or a chart of them. Not the rooms, and not counts"},
	{"config_get_room_statuses_summary", "Checking room status counts", "how many rooms are in each status, plus total and sellable rooms. Not the catalog of status names"},
	{"config_get_properties_id_hk_assignments", "Checking housekeeping", "housekeeper room assignments"},
	{"config_get_properties_id_housekeeping_tasks", "Checking housekeeping tasks", "housekeeping task definitions"},
	{"config_get_properties_id_housekeeping_groups", "Checking housekeeping groups", "housekeeping groups and members"},
	{"reservation_get_reservations", "Checking reservations", "reservation list; query checkInDateStart/checkInDateEnd (arrivals), checkoutDateStart/checkoutDateEnd (departures), entryDate, pageSize. Response has a total"},
	{"reservation_get_reservations_search", "Searching reservations", "find a reservation; query searchKey=guest name or confirmation number"},
	{"reservation_get_reservations_id", "Checking the reservation", "one reservation; path id from a search result"},
	{"reservation_get_reservations_id_payment_summary", "Checking the folio", "balance and payments for one reservation; path id"},
	{"reservation_get_guests", "Searching guests", "guest profiles; query searchKey=name, email or phone"},
	{"reservation_get_guests_guest_id", "Checking the guest", "one guest profile; path guestId from a search result"},
	{"reservation_get_guests_guest_id_reservations", "Checking guest stays", "a guest's reservations; path guestId"},
	{"reservation_get_dashboard_today_summary", "Checking today's summary", "arrivals, departures, in-house, occupancy for a date; query date"},
	{"reservation_get_dashboard_kpi_overview", "Checking KPIs", "ADR, RevPAR, occupancy KPIs for a date; query date"},
	{"reservation_get_dashboard_action_items", "Checking action items", "open action items; query date"},
	{"reservation_get_dashboard_shift_notes", "Checking shift notes", "shift handover notes; query startDate, endDate"},
	{"reservation_get_dashboard_channel_distribution", "Checking channels", "bookings by channel; query startDate, endDate"},
	{"reservation_get_inventory_forecasting", "Checking the forecast", "occupancy, availability, ADR per day; query startDate, endDate"},
	{"config_get_rate_types", "Checking rates", "rate types and base rates; query searchKey"},
	{"config_get_rate_adjustments", "Checking rate adjustments", "rate adjustments; query startDate, endDate"},
	{"config_get_restrictions", "Checking restrictions", "stay restrictions (CTA, CTD, min LOS); query startDate, endDate"},
	{"config_get_cancellation_policies", "Checking policies", "cancellation policies"},
	{"config_get_properties_id_global_settings_summary", "Checking settings", "check-in/check-out times and the Global settings tab; not night audit settings"},
	{"config_get_properties_id_night_audit_settings", "Checking night audit settings", "the Night Audit settings tab: audit time, auto-run, late check-out, payment batch, vacant-to-dirty, selected reports, email recipients"},
	{"reservation_get_night_audit_status", "Checking night audit", "whether tonight's night audit has run (pending, completed, retries, steps); not the settings"},
	{"reservation_get_api_v1_reports_sales_and_revenue", "Checking revenue", "sales and revenue for a period; query startDate, endDate. Not daily collection and not payment types"},
	{"reservation_get_api_v1_reports_daily_collection_by_payment_type", "Checking collections by payment type", "Daily Collection by Payment Type: cash, card, check and other methods for one date; query date (business date for today)"},
	{"reservation_get_api_v1_reports_daily_collection", "Checking daily collection", "Daily Collection report rows for one date; query date (business date for today), pageSize"},
	{"reservation_get_reports_catalog", "Checking reports", "report names and ids. Not the report's figures"},
}

const (
	planTool          = "plan_reads"
	planActionRead    = "read"
	planActionRefuse  = "refuse"
	planActionReply   = "reply"
	maxPlannedReads   = 3
	maxPlanRounds     = 2
	maxReadResultSize = 12000
	maxCandidateReads = 12
	maxErrorDetail    = 240
	maxLabelLength    = 48
	defaultToolLabel  = "Checking property data"
	scopeRefusal      = "I only help with this property in ASI — arrivals, rooms, rates, and reports. I can't answer that."
)

var plannerByName = func() map[string]plannerTool {
	m := make(map[string]plannerTool, len(plannerIndex))
	for _, t := range plannerIndex {
		m[t.Name] = t
	}
	return m
}()

var toolMetaByName = func() map[string]generated.ToolMeta {
	all := generated.AllTools()
	m := make(map[string]generated.ToolMeta, len(all))
	for _, t := range all {
		m[t.Name] = t
	}
	return m
}()

func ToolLabel(name string) string {
	if t, ok := plannerByName[name]; ok {
		return t.Label
	}
	if t, ok := toolMetaByName[name]; ok {
		if s := labelFromDescription(t.Description); s != "" {
			return "Checking " + s
		}
	}
	return defaultToolLabel
}

func labelFromDescription(desc string) string {
	words := strings.Fields(desc)
	for len(words) > 0 {
		switch strings.ToLower(words[0]) {
		case "get", "gets", "return", "returns", "list", "lists", "fetch", "fetches", "retrieve", "retrieves", "all", "a", "an", "the":
			words = words[1:]
			continue
		}
		break
	}
	s := ""
	for _, w := range words {
		if len(s)+len(w)+1 > maxLabelLength {
			break
		}
		s = strings.TrimSpace(s + " " + w)
	}
	if s == "" {
		return ""
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// propertyId is omitted because the server always sets it.
func queryHint(t generated.ToolMeta) string {
	var parts []string
	for _, q := range t.Query {
		if q.Name == "propertyId" {
			continue
		}
		name := q.Name
		if q.Required {
			name += "*"
		}
		if len(q.AllValues) > 0 {
			name += "=" + strings.Join(q.AllValues, "|") + " (all sent unless narrowed)"
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

func readLine(name, use string, t generated.ToolMeta) string {
	line := "- " + name + ": " + use
	if h := queryHint(t); h != "" {
		line += " [query: " + h + "]"
	}
	if p := strings.NewReplacer("/properties/{id}", "", "{propertyId}", "").Replace(t.Path); strings.Contains(p, "{") {
		line += " [path: " + t.Path + "]"
	}
	return line + "\n"
}

type plannedRead struct {
	Tool  string            `json:"tool"`
	Path  map[string]string `json:"path,omitempty"`
	Query map[string]string `json:"query,omitempty"`
}

type readPlan struct {
	Action    string        `json:"action"`
	Reads     []plannedRead `json:"reads,omitempty"`
	FollowUp  bool          `json:"followUp,omitempty"`
	Message   string        `json:"message,omitempty"`
	Entity    string        `json:"entity,omitempty"`
	WriteKind string        `json:"writeKind,omitempty"`
}

type readResult struct {
	Tool   string          `json:"tool"`
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
	Detail string          `json:"detail,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

func planToolDef() genillm.Tool {
	strMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	return genillm.Tool{
		Name:        planTool,
		Description: "Decide how to answer: which property reads to run, or refuse / reply without data.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"action"},
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []any{planActionRead, planActionRefuse, planActionReply},
					"description": "read = run reads; refuse = not about hotels or this property, or a write/payment; reply = a hotel-term definition, a greeting, or a clarifying question"},
				"reads": map[string]any{
					"type": "array", "maxItems": maxPlannedReads,
					"items": map[string]any{
						"type":     "object",
						"required": []string{"tool"},
						"properties": map[string]any{
							"tool":  map[string]any{"type": "string", "description": "a read name from the system prompt lists"},
							"path":  strMap,
							"query": strMap,
						},
					},
				},
				"followUp": map[string]any{"type": "boolean", "description": "true only when another read needs an id from these results"},
				"entity": map[string]any{"type": "string", "enum": createGuideEntityEnum(),
					"description": "only for action=refuse when the user asked to create/add or update/edit one of these (spelled correctly or not); the server replies with the verified ASI screen and ignores message"},
				"writeKind": map[string]any{"type": "string", "enum": []any{writeKindCreate, writeKindUpdate},
					"description": "with entity: create/add vs update/edit/modify; defaults to create"},
				"message": map[string]any{"type": "string", "description": "required for refuse/reply, unless entity is set: the reply to show the user, written for this question. " +
					"For any other write (update, cancel, check-in, payment): say in one sentence that you can't do that action, name the ASI screen where they can, and offer the related read you can do. " +
					"For a non-hotel topic: one sentence that names the topic and says it is outside hotel operations. Do not define it, give an example, or list what you can look up."},
			},
		},
	}
}

func (rc *reqCtx) plannerReads() (core, matched, others string) {
	var c, m strings.Builder
	shown := map[string]bool{}
	for _, t := range plannerIndex {
		meta, ok := rc.e.Tools[t.Name]
		if !ok {
			continue
		}
		shown[t.Name] = true
		c.WriteString(readLine(t.Name, t.Use, meta))
	}
	if rc.e.Selector != nil {
		for _, t := range rc.e.Selector.Select(rc.req.Message, maxCandidateReads) {
			if shown[t.Name] {
				continue
			}
			if _, ok := rc.e.Tools[t.Name]; !ok {
				continue
			}
			shown[t.Name] = true
			m.WriteString(readLine(t.Name, oneLineDesc(t.Description), t))
		}
	}
	var rest []string
	for name := range rc.e.Tools {
		if !shown[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return c.String(), m.String(), strings.Join(rest, ", ")
}

func oneLineDesc(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "no description"
	}
	return s
}

// The whole message must be the greeting.
var greetingRe = regexp.MustCompile(`(?i)^\s*(?:good\s+(morning|afternoon|evening|night)|hi+|hey+|hello+|howdy|yo|namaste)(?:\s+there)?(?:\s+genie)?(?:\s*,?\s*how\s+are\s+you)?[?!.\s]*$`)

func isGreeting(message string) bool {
	return greetingRe.MatchString(message)
}

func daypartForHour(hour int) string {
	switch {
	case hour >= 5 && hour < 12:
		return "Good morning"
	case hour >= 12 && hour < 17:
		return "Good afternoon"
	case hour >= 17 && hour < 22:
		return "Good evening"
	default:
		return "Hello"
	}
}

func firstName(fullName string) string {
	fields := strings.Fields(fullName)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func greetingHour(rc *reqCtx) int {
	if rc.req.LocalHour != nil {
		h := *rc.req.LocalHour
		if h >= 0 && h <= 23 {
			return h
		}
	}
	return time.Now().Hour()
}

func (rc *reqCtx) greetingContext() string {
	if !isGreeting(rc.req.Message) {
		return ""
	}
	name := firstName(rc.currentUser().name)
	if name == "" {
		name = "the signed-in user (name unknown)"
	}
	return fmt.Sprintf("\nGreeting for %s. Time of day: %s.", name, daypartForHour(greetingHour(rc)))
}

func (rc *reqCtx) plannerSystem() string {
	core, matched, others := rc.plannerReads()
	if matched == "" {
		matched = "(none)\n"
	}
	return fmt.Sprintf(`You plan answers for ASI Genie, the assistant inside a hotel property-management system.
Always call %s exactly once.
Scope: operating property %d in ASI, including what hotel terms mean (RevPAR, ADR, occupancy, folio, no-show, walk-in, length of stay, night audit, out of order, rack rate, CTA, CTD). "What is RevPAR?" is in scope: action=read of the KPI or report that holds that figure for the business date, not refuse. action=reply (with the definition in message) when no read has the figure. action=refuse only for non-hotel topics (programming, trivia, weather, news) and for writes, payments, refunds, and check-in/out actions. Someone who says they are in danger and asks what number to call is action=reply: one sentence with the local emergency number and to get a staff member. Do not refuse that.
Read intent, not spelling: "list all the roroms", "arivals tday", "who is sunny" are property questions.
Every refuse/reply carries a message written for this question, except when entity is set. A thanks is one short sentence. A greeting (hi, hello, good morning) is action=reply with a new one-sentence hello you write for this turn: use the name and time of day below, vary the wording every time, and do not list what you can do or mention a property id.%s
A non-hotel refusal is one sentence. Name the topic ("Python is outside hotel operations."). Do not define it, do not give an example, and do not list arrivals, reports, or KPIs. A follow-up to that refusal ("example?", "why?", "tell me more") is still action=refuse in one sentence. Do not continue the topic.
The messages before this one are the conversation. A short confirmation (yes, do it, go ahead, sure, ok, please) means carry out a hotel offer from the previous assistant message. It does not continue a topic you just refused. Read the guest, report, or date named there. Do not start an unrelated lookup.
A follow-up that asks for something you cannot do (another file format, a write, a payment) must leave an earlier download or export in place. Do not say that earlier message was wrong, incorrect, or that no file was made. Say only that the new request isn't available.
For "create/add" or "update/edit/modify" one of <room, room type, rate type, amenity, tax, other charge, space, building, floor, role, reservation, guest, email template, folio notice> (any spelling): action=refuse, set entity to the matching id and writeKind to create or update, and leave message empty - the server sends the verified screen. A generic "update <reservation/guest>" with no name or id has no entity here; write the message yourself instead.
For any other write (cancel, check-in, payment, refund), write the message yourself: say which ASI screen does it and what you can show instead.
A person's name may be a guest or a staff user: read reservation_get_guests and config_get_properties_id_users together.
"Today" is the business date %s. Resolve relative dates to YYYY-MM-DD.
Pick the fewest reads (at most %d). propertyId and /properties/{id} are filled for you. Use pageSize 100 for lists.
Any read listed below may be used: the core reads, the reads matching this question, or any other allowed read by name. [query: ...] lists a read's params; * marks required. A required date param left out defaults to the business date (startDate/endDate copy each other first). A param shown as name=val1|val2|... (all sent unless narrowed) is already sent with every value when you leave it out, so "show me the report" needs no query at all; set it yourself, to only the matching value(s), only when the user named a subset (e.g. "only cash", "just the checked-out ones").
A named report, screen, or setting (any spelling) uses the read whose name matches it, never a different one: a collection report is not sales and revenue, settings are not status. Questions about which room statuses exist, their colors, or a chart of them use config_get_room_statuses. That is not the list of rooms and not the status counts. If no listed read matches, action=reply naming what you can't find.
When a read returns ids the user would see and this round has no name for them, set followUp and read the list that carries the names (reports catalog, room types, rate types, users, guests) before answering. Night audit settings are rewritten for you: selectedReports is already the report names.
If a result has status error with a detail, fix the params in a follow-up round when the detail says what was wrong.
Core reads:
%s
Reads matching this question:
%s
Other allowed reads (by name):
%s`, planTool, rc.pid, rc.greetingContext(), rc.bizDate, maxPlannedReads, core, matched, others)
}

const answerSystem = `You are ASI Genie. Answer the user's question from the JSON read results, by calling present_answer exactly once.
The messages before the question are the conversation, including your previous reply. A short confirmation (yes, do it, go ahead, sure, ok) means do what that reply offered. Do not say the request names no action when the previous reply offered one.
Do not say an earlier download or export was wrong or that no file was made. If this question asks for something the results do not include, say that isn't available and leave the earlier file in place.
Copy numbers and names exactly; never invent a figure. When a list response has a total, use it for counts.
If the user asked what a hotel term means (RevPAR, ADR, occupancy, folio, and similar), the first sentence is a plain definition. Any number after that must come from the JSON; if the JSON has no figure, stop after the definition.
If a read failed or returned nothing, say what you couldn't find in summary and send no views.
summary: one or two plain sentences with the direct answer. Never put a table or bullet list in summary.
Every view needs a title: a short name of what it shows (the report, the metric, or the person), such as "Daily Collection by Payment Type" or "Cash". Never title a view "ASI Genie", "Report", "Table", or "Chart".
Pick the views that fit the data (any combination):
- stats: 2-8 headline numbers (counts, occupancy, ADR, revenue, balances).
- chart: a trend over dates or a breakdown by category with 3+ points (bar for categories, line/area for dates, pie for shares of a whole). Values must be numbers.
- table: records with several fields (reservations, guests, rooms, users, rates). Choose 3-7 useful columns, at most 50 rows.
- list: short one-line items (notes, tasks, policies) with optional badges.
- detail: label/value facts about one record (a guest, a reservation, the signed-in user).
Format dates as YYYY-MM-DD (or "YYYY-MM-DD HH:mm" when the time matters); never raw ISO timestamps. Show money with 2 decimals.
Show a name, never a raw database id, wherever the JSON has a name for it (selectedReports, roomTypeName, rateTypeName, a guest name, a user name). Room numbers, confirmation numbers, counts, money, and times stay. Do not print selectedReportIds, roomTypeId, rateTypeId, userId, guestId, or any other database id.
Follow the shape the user asked for: a chart request gets a chart, a table request gets a table, and a list request ("list", "names", "show all", "what ... do we have") gets a list or table with one item per record in the JSON. Never answer a list request with only a count or a summary, and do not group or pick examples instead of listing every item. Room status rows include color and isPreset; mention that a status is preset only when that field is true, and do not add a fixed explanation about presets.
Do not mention tools, APIs, JSON or endpoints.`

const suggestionAnswerHint = `
Also set suggestions to 2 or 3 next requests the user can send. Tapping one sends that message and Genie performs it. Only property reads (arrivals, a balance, a guest, a report) or email, print, or download of a folio already named in the results. Copy names and reservation numbers. Do not suggest payments, refunds, check-in, or creating a reservation. If the answer says a folio was already sent, do not suggest sending it again.`

func answerPrompt() string {
	if !suggestionsEnabled() {
		return answerSystem
	}
	return answerSystem + suggestionAnswerHint
}

func (rc *reqCtx) routerOwnsTurn() bool {
	m := rc.req.Message
	if _, ok := parseStructuredDownload(m); ok || messageWantsFileExport(m) {
		return true
	}
	// "in pdf?" after a file card is still that download, not a new planner turn.
	if _, ok := exportFormatFollowUp(m); ok && rc.hasPriorFileOffer() {
		return true
	}
	// Folio download and email are not data reads: the planner has no confirm card or write tool.
	if messageWantsFolioAction(m) {
		return true
	}
	if rc.isBareFolioPrintFollowUp() {
		return true
	}
	// A confirm reply has no folio keyword. Restore the saved send so the planner cannot refuse it.
	if isEmailFolioConfirmReply(m) && rc.restoreEmailFolioEntity() {
		if _, ok := decodeEmailFolioPending(rc.sess.LastEntity); ok {
			return true
		}
	}
	if isEmailFolioConfirmReply(m) {
		if _, ok := emailConfirmFromHistory(rc.req.History); ok {
			return true
		}
	}
	if _, ok := decodeEmailFolioSent(rc.sess.LastEntity); ok && messageAsksFolioEmailStatus(m) {
		return true
	}
	if parseGuestIDFromLookupMessage(m) != "" {
		return true
	}
	return rc.pendingClarifyIntentFromHistory() != "" && !looksLikeNewQuestion(m)
}

func (e *Engine) planAndAnswer(rc *reqCtx) bool {
	msgs := appendUserTurn(rc.recentTurns(), rc.req.Message)
	var results []readResult
	for round := 0; round < maxPlanRounds; round++ {
		plan, ok := e.plan(rc, msgs)
		if !ok {
			return round > 0 && e.answerFromReads(rc, results)
		}
		if plan.Action == planActionRefuse {
			// A hotel term stays in scope even when the model files it as trivia.
			if read, ok := hotelConceptRead(rc.req.Message, rc.bizDate); ok {
				plan = readPlan{Action: planActionRead, Reads: []plannedRead{read}}
			} else if guide, ok := createGuideByID[plan.Entity]; ok && guide.matches(firstNonEmpty(plan.WriteKind, writeKindCreate)) {
				// A verified ASI screen beats a path the model guesses.
				writeKind := firstNonEmpty(plan.WriteKind, writeKindCreate)
				rc.setIntent(IntentInfo{ID: "unsupported", Handler: "planner", Source: "rule"})
				rc.add(cards.Navigate("unsupported", guide.title(writeKind), guide.link(writeKind, rc.pid)))
				rc.text(guide.message(writeKind))
				return true
			} else if isHotelConcept(rc.req.Message) {
				return e.explainHotelConcept(rc)
			} else {
				rc.setIntent(IntentInfo{ID: "unsupported", Handler: "planner", Source: "llm"})
				// The constant is only the fallback when the model sent an empty message.
				rc.text(firstNonEmpty(strings.TrimSpace(plan.Message), scopeRefusal))
				return true
			}
		}
		switch plan.Action {
		case planActionReply:
			rc.setIntent(IntentInfo{ID: "smalltalk", Handler: "planner", Source: "llm"})
			rc.text(firstNonEmpty(strings.TrimSpace(plan.Message), "Hi! Ask me about arrivals, rooms, rates or reports for this property."))
			return true
		}
		if len(plan.Reads) == 0 {
			return round > 0 && e.answerFromReads(rc, results)
		}
		batch := rc.runReads(plan.Reads)
		results = append(results, batch...)
		if !plan.FollowUp && !needsParamFix(batch) {
			break
		}
		payload, err := json.Marshal(batch)
		if err != nil {
			slog.Warn("genie follow-up payload failed; answering with first reads", "err", err, "propertyId", rc.pid)
			break
		}
		msgs = append(msgs,
			genillm.Message{Role: genillm.RoleAssistant, Content: "Read results so far."},
			genillm.Message{Role: genillm.RoleUser, Content: "Results: " + string(payload) + "\nPlan any follow-up reads that need ids from these results or retry a failed read with corrected params, or action=read with no reads if you have enough."})
	}
	return e.answerFromReads(rc, results)
}

func (e *Engine) plan(rc *reqCtx, msgs []genillm.Message) (readPlan, bool) {
	ctx, cancel := rc.llmCtx()
	res, err := e.LLM.SendMessage(ctx, rc.plannerSystem(), msgs, []genillm.Tool{planToolDef()})
	cancel()
	if err != nil {
		rc.llmFailed = true
		slog.Warn("genie planner call failed; using intent router", "err", err, "propertyId", rc.pid)
		return readPlan{}, false
	}
	for _, tc := range res.ToolCalls {
		if tc.Name != planTool {
			continue
		}
		var p readPlan
		if err := json.Unmarshal(tc.Arguments, &p); err != nil {
			slog.Warn("genie planner returned invalid plan", "err", err, "propertyId", rc.pid)
			return readPlan{}, false
		}
		return p, true
	}
	slog.Warn("genie planner did not call plan_reads", "propertyId", rc.pid)
	return readPlan{}, false
}

func (rc *reqCtx) runReads(reads []plannedRead) []readResult {
	if len(reads) > maxPlannedReads {
		reads = reads[:maxPlannedReads]
	}
	out := make([]readResult, len(reads))
	var g errgroup.Group
	for i, r := range reads {
		g.Go(func() error {
			out[i] = rc.runRead(r)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		slog.Warn("genie planned reads failed", "err", err, "propertyId", rc.pid)
	}
	return out
}

func (rc *reqCtx) runRead(r plannedRead) readResult {
	res := readResult{Tool: r.Tool}
	// e.Tools holds only allowed GETs, so a model-chosen write or sensitive read cannot run.
	t, ok := rc.e.Tools[r.Tool]
	if !ok {
		slog.Warn("genie planner asked for unavailable tool", "tool", r.Tool, "propertyId", rc.pid)
		res.Status, res.Error = "skipped", "not_available"
		return res
	}
	pathParams := map[string]string{}
	for k, v := range r.Path {
		pathParams[k] = v
	}
	if strings.Contains(t.Path, "/properties/{id}") {
		pathParams["id"] = rc.pidStr()
	}
	if strings.Contains(t.Path, "{propertyId}") {
		pathParams["propertyId"] = rc.pidStr()
	}
	qv := url.Values{}
	for k, v := range r.Query {
		if strings.TrimSpace(v) != "" {
			qv.Set(k, v)
		}
	}
	// Swagger names this param "key"; the service binds searchKey and ignores key.
	if r.Tool == "reservation_get_reservations_search" && qv.Get("searchKey") == "" && qv.Get("key") != "" {
		qv.Set("searchKey", qv.Get("key"))
		qv.Del("key")
	}
	if t.Service != "auth" || hasQueryParam(t, "propertyId") {
		qv.Set("propertyId", rc.pidStr())
	}
	fillRequiredDates(t, qv, rc.bizDate)
	fillEnumDefaults(t, qv)
	if missing := missingRequiredQuery(t, qv); len(missing) > 0 {
		// Don't send a call that would 400; the follow-up round can supply the missing param.
		slog.Warn("genie planned read missing required params", "tool", r.Tool, "missing", missing, "propertyId", rc.pid)
		res.Status, res.Error = "error", "missing_parameter"
		res.Detail = "required query params not set: " + strings.Join(missing, ", ")
		return res
	}
	b, err := rc.getRaw(r.Tool, pathParams, qv)
	if err != nil {
		slog.Warn("genie planned read failed", "err", err, "tool", r.Tool, "propertyId", rc.pid)
		res.Status, res.Error = "error", errorCode(err)
		res.Detail = clientErrorDetail(err)
		return res
	}
	res.Status = "executed"
	if r.Tool == nightAuditSettingsTool {
		b = rc.nameNightAuditReports(b)
	}
	if len(b) > maxReadResultSize {
		b, _ = json.Marshal(string(b[:maxReadResultSize]) + "…[truncated: data is partial]")
	}
	res.Data = asJSON(b)
	return res
}

func hasQueryParam(t generated.ToolMeta, name string) bool {
	for _, q := range t.Query {
		if q.Name == name {
			return true
		}
	}
	return false
}

// Optional dates stay unset so the API keeps its own default range.
func fillRequiredDates(t generated.ToolMeta, qv url.Values, bizDate string) {
	partner := map[string]string{"startDate": "endDate", "endDate": "startDate", "fromDate": "toDate", "toDate": "fromDate"}
	for _, q := range t.Query {
		if !q.Required || qv.Get(q.Name) != "" || !isDateParam(q.Name) {
			continue
		}
		if p, ok := partner[q.Name]; ok && qv.Get(p) != "" {
			qv.Set(q.Name, qv.Get(p))
			continue
		}
		if bizDate != "" {
			qv.Set(q.Name, bizDate)
		}
	}
}

func isDateParam(name string) bool {
	n := strings.ToLower(name)
	return n == "date" || strings.HasSuffix(n, "date")
}

// An unset comma-separated filter is sent as every value. Several reports treat a missing param as match-nothing.
func fillEnumDefaults(t generated.ToolMeta, qv url.Values) {
	for _, q := range t.Query {
		if len(q.AllValues) == 0 || qv.Get(q.Name) != "" {
			continue
		}
		qv.Set(q.Name, strings.Join(q.AllValues, ","))
	}
}

func missingRequiredQuery(t generated.ToolMeta, qv url.Values) []string {
	var missing []string
	for _, q := range t.Query {
		if q.Required && qv.Get(q.Name) == "" {
			missing = append(missing, q.Name)
		}
	}
	return missing
}

// Card numbers are masked. Only 400/422 details are returned so other statuses never reach the model.
func clientErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	i := strings.Index(msg, "HTTP 400: ")
	if i < 0 {
		i = strings.Index(msg, "HTTP 422: ")
	}
	if i < 0 {
		return ""
	}
	detail, _ := maskPAN(strings.TrimSpace(msg[i+len("HTTP 400: "):]))
	if len(detail) > maxErrorDetail {
		detail = detail[:maxErrorDetail] + "…"
	}
	return detail
}

func needsParamFix(batch []readResult) bool {
	for _, r := range batch {
		if r.Status == "error" && r.Detail != "" {
			return true
		}
	}
	return false
}

func (e *Engine) answerFromReads(rc *reqCtx, results []readResult) bool {
	rc.setIntent(IntentInfo{ID: "agent", Handler: "planner", Source: "llm"})
	if allDenied(results) {
		rc.add(cards.Denied("agent", ""))
		rc.text("That isn't available for your role.")
		return true
	}
	if card, text, ok := outageCard(results); ok {
		rc.add(card)
		rc.text(text)
		return true
	}
	payload, err := json.Marshal(results)
	if err != nil {
		slog.Error("genie answer payload failed", "err", err, "propertyId", rc.pid)
		return false
	}
	ctx, cancel := rc.llmCtx()
	res, err := e.LLM.SendMessage(ctx, answerPrompt(), appendUserTurn(rc.recentTurns(), fmt.Sprintf(
		"Business date: %s\nQuestion: %s\nRead results: %s", rc.bizDate, rc.req.Message, payload)), []genillm.Tool{presentToolDef()})
	cancel()
	if err != nil {
		rc.llmFailed = true
		slog.Warn("genie answer call failed; using intent router", "err", err, "propertyId", rc.pid)
		rc.resp.ToolCalls = nil
		return false
	}
	for _, tc := range res.ToolCalls {
		if tc.Name != presentTool {
			continue
		}
		var a presentedAnswer
		if err := json.Unmarshal(tc.Arguments, &a); err != nil {
			slog.Warn("genie present_answer arguments invalid; using text", "err", err, "propertyId", rc.pid)
			break
		}
		if rc.present(a) {
			// The model copies labels, not photo URLs.
			attachPersonImages(rc.resp.Cards, results)
			rc.ensureRequestedList(results)
			return true
		}
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		slog.Warn("genie answer call returned no text", "propertyId", rc.pid)
		rc.resp.ToolCalls = nil
		return false
	}
	rc.text(text)
	rc.ensureRequestedList(results)
	return true
}

func allDenied(results []readResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if r.Status != "error" || (r.Error != "forbidden" && r.Error != "session_expired") {
			return false
		}
	}
	return true
}
