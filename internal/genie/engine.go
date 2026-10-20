// Package genie is the Phase 1 (read-only, deterministic) Genie engine:
// LLM intent classification + slot extraction → deterministic
// handler (fixed GET reads with the user's token, server-side composition) →
// typed cards. RBAC is enforced by the ASI APIs (401/403 → Denied card).
package genie

import (
	"asi-genie-poc/internal/logx"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
	"asi-genie-poc/internal/store"
	genillm "asi-genie-poc/llm"
	"asi-genie-poc/toolregistry/generated"
)

const (
	ToolTimeout  = 20 * time.Second
	contextTurns = 6
)

// Request is the /chat request body.
type Request struct {
	SessionID    string            `json:"sessionId,omitempty"`
	Message      string            `json:"message"`
	GroupID      int               `json:"groupId,omitempty"`      // rejected: no group chat
	PropertyID   int               `json:"propertyId,omitempty"`   // required: the property the user is on (body or X-Property-Id)
	BusinessDate string            `json:"businessDate,omitempty"` // YYYY-MM-DD (optional; read from night-audit working date)
	LocalHour    *int              `json:"localHour,omitempty"`    // 0-23 on the user's clock, for greetings
	Screen       string            `json:"screen,omitempty"`       // current route (optional)
	History      []genillm.Message `json:"history,omitempty"`
}

// IntentInfo reports how the message was understood.
type IntentInfo struct {
	ID           string  `json:"id"`
	ScenarioID   string  `json:"scenarioId,omitempty"`
	ResponseType string  `json:"responseType,omitempty"`
	Handler      string  `json:"handler"` // dedicated | generic | navigate | guide | fallback
	Confidence   float64 `json:"confidence,omitempty"`
	Source       string  `json:"source"` // llm | keyword | rule
	Slots        Slots   `json:"slots,omitempty"`
}

// ToolCallStatus reports a backend read made for this reply.
type ToolCallStatus struct {
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args"`
	Status     string          `json:"status"`          // executed | error | skipped
	Error      string          `json:"error,omitempty"` // error code only (raw errors are logged, never returned)
	DurationMs int64           `json:"durationMs"`
}

// Response is the /chat response body (see docs/response-contract.md).
type Response struct {
	SessionID    string           `json:"sessionId,omitempty"`
	Text         string           `json:"text"`
	Cards        []cards.Card     `json:"cards,omitempty"`
	Chips        []cards.Chip     `json:"chips,omitempty"`
	Citations    []cards.Citation `json:"citations,omitempty"`
	Intent       *IntentInfo      `json:"intent,omitempty"`
	PropertyID   int              `json:"propertyId,omitempty"`
	BusinessDate string           `json:"businessDate,omitempty"`
	ToolCalls    []ToolCallStatus `json:"toolCalls,omitempty"`
	Notice       string           `json:"notice,omitempty"` // e.g. "The AI is busy, I used quick matching"
	CreatedAt    time.Time        `json:"createdAt"`        // when the reply was produced (UTC, ISO 8601)
	DurationMs   int64            `json:"durationMs"`       // server time to answer
	TurnID       string           `json:"turnId,omitempty"` // analytics row for feedback
}

// Entity is the last thing the conversation referred to (context carry, G-097).
type Entity struct {
	Kind  string `json:"kind"` // reservation | guest | room
	ID    string `json:"id"`
	Label string `json:"label"`
}

type session struct {
	PropertyID   int
	PropertyName string
	LastEntity   *Entity
	LastIntent   string
	LastSeen     time.Time
	PrevSeen     time.Time
}

// Engine wires everything together. Create with New.
type Engine struct {
	LLM      genillm.LLMClient
	HTTP     *generated.Client
	Tools    map[string]generated.ToolMeta // GET read-only tools only
	Selector *ToolSelector
	Store    store.Store

	mu       sync.Mutex
	sessions map[string]*session
	users    map[string]userInfo
	Model    string // LLM model name, recorded per turn
	bizDates map[int]dateEntry
	handlers map[string]handlerFunc
}

type dateEntry struct {
	date string
	at   time.Time
}

// New builds an engine. Writes and sensitive GETs (admin, impersonation,
// invite, internal, audit-log, export) are dropped and can never be called.
func New(client genillm.LLMClient, st store.Store) *Engine {
	read := []generated.ToolMeta{}
	by := map[string]generated.ToolMeta{}
	for _, t := range generated.AllTools() {
		if IsAllowedReadTool(t) {
			read = append(read, t)
			by[t.Name] = t
		}
	}
	e := &Engine{
		LLM: client, HTTP: generated.NewClient(), Tools: by, Selector: NewToolSelector(read),
		Store: st, sessions: map[string]*session{}, bizDates: map[int]dateEntry{}, users: map[string]userInfo{},
	}
	e.handlers = e.dedicatedHandlers()
	return e
}

// reqCtx carries per-request state.
type reqCtx struct {
	mu             sync.Mutex
	llmDeadline    time.Time // shared LLM budget for this request
	llmFailed      bool      // classifier LLM failed: don't call the LLM again this request
	start          time.Time
	lastErrService string // service of the last failed ASI call (for error wording)
	errCode        string
	errMsg         string
	e              *Engine
	ctx            context.Context
	token          string
	req            Request
	sess           *session
	pid            int
	bizDate        string
	resp           *Response
	intent         intents.Intent
}

// session returns the in-memory context for id. A session is bound to one
// property: if the property changes, its context starts fresh.
func (e *Engine) session(id string, pid int) *session {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == "" {
		return &session{PropertyID: pid}
	}
	s, ok := e.sessions[id]
	if !ok || s.PropertyID != pid {
		s = &session{PropertyID: pid}
		e.sessions[id] = s
	}
	return s
}

// ---- PCI (G-095) ----

// ---- analytics turn ----

type userInfo struct {
	id, name, email, role string
	propertyIDs           []int
	at                    time.Time
}

func (u userInfo) allows(propertyID int) bool {
	for _, id := range u.propertyIDs {
		if id == propertyID {
			return true
		}
	}
	return false
}

// currentUser returns the caller, cached 10 min per token. Errors are ignored;
// Authorize uses loadCaller when a failure must be returned to the client.
func (rc *reqCtx) currentUser() userInfo {
	u, _ := rc.loadCaller()
	return u
}

// loadCaller reads auth current-user and the property ids on GET /permissions.
// A successful result is cached for 10 minutes. Failures are not cached.
func (rc *reqCtx) loadCaller() (userInfo, error) {
	if strings.TrimSpace(rc.token) == "" {
		return userInfo{}, ErrUnauthenticated
	}
	h := sha256.Sum256([]byte(rc.token))
	key := hex.EncodeToString(h[:8])
	rc.e.mu.Lock()
	u, ok := rc.e.users[key]
	rc.e.mu.Unlock()
	if ok && u.id != "" && time.Since(u.at) < 10*time.Minute {
		return u, nil
	}
	v, err := rc.get("auth_get_current_user", nil, nil)
	if err != nil {
		return userInfo{}, err
	}
	u = userInfo{at: time.Now()}
	u.id = deepStr(v, "id", "userId")
	u.email = deepStr(v, "email")
	u.role = deepStr(v, "roleName")
	u.name = strings.TrimSpace(deepStr(v, "firstName") + " " + deepStr(v, "lastName"))
	if u.name == "" {
		u.name = deepStr(v, "name", "fullName")
	}
	if u.id == "" {
		return userInfo{}, ErrUnauthenticated
	}
	perms, err := rc.get("auth_get_permissions", nil, nil)
	if err != nil {
		return userInfo{}, err
	}
	u.propertyIDs = propertyIDsOf(perms)
	rc.e.mu.Lock()
	rc.e.users[key] = u
	rc.e.mu.Unlock()
	return u, nil
}

func newSessionID(propertyID int) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("web-p%d-%s", propertyID, hex.EncodeToString(b))
}

func (e *Engine) logTurn(rc *reqCtx) string {
	nTools := len(rc.resp.ToolCalls)
	u := rc.currentUser()
	// The current-user lookup isn't part of the answer: keep it out of the reply's tool list.
	rc.resp.ToolCalls = rc.resp.ToolCalls[:nTools]
	t := store.Turn{SessionID: rc.req.SessionID, PropertyID: rc.pid, UserID: u.id, UserName: u.name, UserEmail: u.email,
		Message: rc.req.Message, Reply: rc.resp.Text, LLMModel: e.Model, LLMFailed: rc.llmFailed,
		DurationMs: rc.resp.DurationMs, ErrorCode: rc.errCode, ErrorMessage: logx.Mask(rc.errMsg), CreatedAt: rc.resp.CreatedAt}
	t.Message, _ = logx.MaskPAN(t.Message)
	if len(rc.resp.Cards) > 0 {
		t.Cards, _ = json.Marshal(rc.resp.Cards)
	}
	if it := rc.resp.Intent; it != nil {
		t.Intent, t.ScenarioID, t.Source, t.Confidence = it.ID, it.ScenarioID, it.Source, it.Confidence
		t.Slots, _ = json.Marshal(it.Slots)
	}
	if nTools > 0 {
		type tl struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Error      string `json:"error,omitempty"`
			DurationMs int64  `json:"durationMs"`
		}
		ts := make([]tl, 0, nTools)
		for _, c := range rc.resp.ToolCalls {
			ts = append(ts, tl{c.Name, c.Status, c.Error, c.DurationMs})
		}
		t.Tools, _ = json.Marshal(ts)
	}
	if t.Source == "" && e.LLM == nil {
		t.Source = "keyword"
	}
	id, err := e.Store.LogTurn(context.WithoutCancel(rc.ctx), t)
	if err != nil {
		log.Printf("store turn: %v", err)
		return ""
	}
	rc.resp.TurnID = id
	return id
}

// maskPAN replaces card-number-like sequences; reports whether any were found.
func maskPAN(s string) (string, bool) { return logx.MaskPAN(s) }

// ErrPropertyRequired: /chat needs the property the user is on (no group chat).
var ErrPropertyRequired = errors.New("propertyId is required: Genie is property-specific (group chat isn't supported)")

// Handle answers one chat turn.
func (e *Engine) Handle(ctx context.Context, token string, req Request) (*Response, error) {
	req.Message = strings.TrimSpace(req.Message)
	if req.PropertyID > 0 && req.SessionID == "" {
		req.SessionID = newSessionID(req.PropertyID)
	}
	sess := e.session(req.SessionID, req.PropertyID)
	rc := &reqCtx{e: e, ctx: ctx, token: token, req: req, sess: sess, resp: &Response{SessionID: req.SessionID}, start: time.Now()}
	e.mu.Lock()
	sess.PrevSeen, sess.LastSeen = sess.LastSeen, time.Now()
	e.mu.Unlock()

	// 1) PCI: never send card numbers to the model or the store.
	masked, hasPAN := maskPAN(req.Message)
	if hasPAN {
		rc.req.Message = masked
		rc.setIntent(IntentInfo{ID: "pci_block", ScenarioID: "G-095", ResponseType: intents.RTDenied, Handler: "dedicated", Source: "rule"})
		c := cards.Card{Type: cards.TypeDenied, Intent: "pci_block", Title: "Card numbers can't be shared in chat",
			Message: "For security I removed that card number. Take payments only through the card terminal or the payment screen."}
		rc.add(c)
		rc.text("I can't accept card numbers in chat. I've masked it - please use the payment terminal flow.")
		e.logEvent(rc, "pci_block", "pci_block", "G-095", "")
		e.persist(rc)
		return rc.resp, nil
	}

	// 2) Active property: required (Genie is property-specific; no group chat).
	if req.PropertyID <= 0 {
		return nil, ErrPropertyRequired
	}
	rc.pid = req.PropertyID
	rc.resp.PropertyID = rc.pid
	// Keyword refusals only when no model is configured; otherwise the planner
	// reads the question and writes a reply that fits it.
	if e.LLM == nil && isReadOnlyRefusal(rc.req.Message) {
		rc.setIntent(IntentInfo{ID: intents.UnsupportedID, Handler: "fallback", Source: "rule"})
		rc.add(cards.NotSupported(intents.UnsupportedID, "", nil))
		rc.text("I only help with this property in ASI — arrivals, rooms, rates, and reports. I can't answer that.")
		e.persist(rc)
		return rc.resp, nil
	}
	// Working date before routing so "today" is the night-audit date, not the server clock.
	rc.bizDate = e.businessDate(rc)
	rc.resp.BusinessDate = rc.bizDate

	// 3) Model-first: the LLM plans the reads and writes the answer. The intent
	// router below only runs when the planner can't (LLM error, exports, clarify replies).
	if e.LLM != nil && !rc.routerOwnsTurn() && e.planAndAnswer(rc) {
		e.mu.Lock()
		sess.LastIntent = rc.resp.Intent.ID
		e.mu.Unlock()
		e.logEvent(rc, "intent", rc.resp.Intent.ID, "", "")
		e.persist(rc)
		return rc.resp, nil
	}

	// 4) Understand: intent + slots.
	it, info, slots := e.classify(rc)
	if gid := parseGuestIDFromLookupMessage(rc.req.Message); gid != "" {
		if it2, ok := intents.Get("lookup_guest"); ok {
			it, info = it2, IntentInfo{ID: it2.ID, ScenarioID: it2.ScenarioID, ResponseType: it2.ResponseType, Handler: "dedicated", Source: "rule", Confidence: 1}
		}
	}
	rc.resolveSlotDates(&slots)
	rc.applyContext(&slots)
	it, info, slots = e.resolveFollowUpIntent(rc, it, info, slots)
	it, info, slots = e.applyDownloadIntent(it, info, slots, rc.req.Message)
	rc.intent = it
	info.Slots = slots
	rc.setIntent(info)
	rc.resp.Intent.Slots = slots

	// 7) Dispatch.
	if err := e.dispatch(rc, it, slots, info); err != nil {
		// Never a raw error: a readable error card (401 → session expired,
		// 403 → no access, timeouts → service didn't respond), raw text to the logs.
		log.Printf("genie: handler %s: %v", it.ID, err)
		rc.resp.Cards = nil
		link := moduleLink(it.Module, rc.pid)
		c, reply := errorCard(it, err, rc.lastErrService, &link)
		rc.add(c)
		rc.resp.Text = reply
		rc.errCode, rc.errMsg = c.Code, err.Error()
		if c.Code == "forbidden" || c.Code == "session_expired" {
			e.logEvent(rc, "denied", it.ID, it.ScenarioID, c.Code)
		}
	}
	rc.phraseResponse()
	if rc.llmFailed {
		rc.resp.Notice = llmBusyNote
	}
	e.mu.Lock()
	sess.LastIntent = it.ID
	e.mu.Unlock()
	e.logEvent(rc, "intent", it.ID, it.ScenarioID, "")
	e.persist(rc)
	return rc.resp, nil
}

func isReadOnlyRefusal(message string) bool {
	m := strings.ToLower(strings.TrimSpace(message))
	for _, word := range []string{
		"python", "pythn", "refund", "refunds", "take payment", "process payment", "charge card", "make payment",
		"create reservation", "book a room", "cancel reservation", "check in ", "check out ", "mark room", "update room status",
	} {
		if strings.Contains(m, word) {
			return true
		}
	}
	return false
}

func (rc *reqCtx) setIntent(i IntentInfo) { rc.resp.Intent = &i }

func (rc *reqCtx) add(c cards.Card) {
	c.Finalize()
	rc.resp.Cards = append(rc.resp.Cards, c)
}

func (rc *reqCtx) text(s string) {
	if rc.resp.Text == "" {
		rc.resp.Text = s
	} else {
		rc.resp.Text += "\n" + s
	}
}

func (rc *reqCtx) chips(labels ...string) {
	for _, l := range labels {
		rc.resp.Chips = append(rc.resp.Chips, cards.Chip{Label: l, Message: l})
	}
}

func (rc *reqCtx) remember(ent Entity) {
	rc.e.mu.Lock()
	rc.sess.LastEntity = &ent
	rc.e.mu.Unlock()
}

// businessDate returns the property's working (business) date, cached 10 min.
func (e *Engine) businessDate(rc *reqCtx) string {
	if d := rc.req.BusinessDate; len(d) == 10 {
		return d
	}
	e.mu.Lock()
	if de, ok := e.bizDates[rc.pid]; ok && time.Since(de.at) < 10*time.Minute {
		e.mu.Unlock()
		return de.date
	}
	e.mu.Unlock()
	d := ""
	if v, err := rc.get("reservation_get_night_audit_working_date", nil, url.Values{"propertyId": {rc.pidStr()}}); err == nil {
		d = firstDate(v)
	}
	if d == "" {
		d = time.Now().Format("2006-01-02")
	} else {
		e.mu.Lock()
		e.bizDates[rc.pid] = dateEntry{d, time.Now()}
		e.mu.Unlock()
	}
	return d
}

var dateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

func firstDate(v any) string {
	if s, ok := v.(string); ok {
		return dateRe.FindString(s)
	}
	for _, k := range []string{"workingDate", "businessDate", "date", "lastWorkingDate"} {
		if s := deepStr(v, k); s != "" {
			if d := dateRe.FindString(s); d != "" {
				return d
			}
		}
	}
	b, _ := json.Marshal(v)
	return dateRe.FindString(string(b))
}

func (rc *reqCtx) pidStr() string { return strconv.Itoa(rc.pid) }

// ---- backend reads ----

// LLMTimeout is the total LLM budget per /chat request (LLM_TIMEOUT, default
// 30s), shared by the classifier and the generic handler.
var LLMTimeout = envDuration("LLM_TIMEOUT", 30*time.Second)

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil && d > 0 {
		return d
	}
	return def
}

// llmCtx returns a context bounded by this request's shared LLM budget.
func (rc *reqCtx) llmCtx() (context.Context, context.CancelFunc) {
	if rc.llmDeadline.IsZero() {
		rc.llmDeadline = time.Now().Add(LLMTimeout)
	}
	return context.WithDeadline(rc.ctx, rc.llmDeadline)
}

var errNoTool = errors.New("no read endpoint available")

// isAuthErr reports whether an ASI call failed with 401/403 (generated client error text).
func isAuthErr(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "HTTP 401") || strings.Contains(m, "HTTP 403")
}

// get calls a generated GET tool by name with the caller's token.
func (rc *reqCtx) get(toolName string, pathParams map[string]string, q url.Values) (any, error) {
	b, err := rc.getRaw(toolName, pathParams, q)
	if err != nil {
		return nil, err
	}
	return decode(b), nil
}

func (rc *reqCtx) getRaw(toolName string, pathParams map[string]string, q url.Values) ([]byte, error) {
	t, ok := rc.e.Tools[toolName]
	argsJSON, _ := json.Marshal(map[string]any{"path": pathParams, "query": q})
	st := ToolCallStatus{Name: toolName, Args: argsJSON}
	if !ok {
		st.Status, st.Error = "skipped", "not_available"
		rc.mu.Lock()
		rc.resp.ToolCalls = append(rc.resp.ToolCalls, st)
		rc.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", toolName, errNoTool)
	}
	p := t.Path
	for k, v := range pathParams {
		p = strings.ReplaceAll(p, "{"+k+"}", url.PathEscape(v))
	}
	if strings.Contains(p, "{") {
		st.Status, st.Error = "error", "missing_parameter"
		rc.mu.Lock()
		rc.resp.ToolCalls = append(rc.resp.ToolCalls, st)
		rc.mu.Unlock()
		return nil, fmt.Errorf("%s: missing path parameter in %s", toolName, p)
	}
	t0 := time.Now()
	b, err := rc.e.execTool(rc.ctx, rc.req.SessionID, rc.token, t, p, q, argsJSON)
	st.DurationMs = time.Since(t0).Milliseconds()
	if err != nil {
		st.Status, st.Error = "error", errorCode(err)
		rc.mu.Lock()
		rc.lastErrService = t.Service
		rc.mu.Unlock()
	} else {
		st.Status = "executed"
	}
	rc.mu.Lock()
	rc.resp.ToolCalls = append(rc.resp.ToolCalls, st)
	rc.mu.Unlock()
	return b, err
}

// phraseResponse turns deterministic card JSON into a concise lead-in without
// exposing endpoint schemas or giving the model any tools to call.
func (rc *reqCtx) phraseResponse() {
	// A handler that already wrote the sentence keeps it. Replacing it repeats
	// the card text in the chat bubble.
	if strings.TrimSpace(rc.resp.Text) != "" {
		return
	}
	if rc.e.LLM == nil || rc.llmFailed || len(toolsForIntent(rc.intent.ID, rc.resp.Intent.Slots)) == 0 || len(rc.resp.Cards) == 0 {
		return
	}
	payload, err := json.Marshal(struct {
		Cards []cards.Card `json:"cards"`
	}{Cards: rc.resp.Cards})
	if err != nil {
		slog.Warn("genie response phrasing payload failed", "err", err)
		return
	}
	ctx, cancel := rc.llmCtx()
	res, err := rc.e.LLM.SendMessage(ctx,
		"Write one short markdown sentence using only the supplied JSON. Copy numbers exactly. Do not add facts.",
		[]genillm.Message{{Role: genillm.RoleUser, Content: string(payload)}}, nil)
	cancel()
	if err != nil {
		slog.Warn("genie response phrasing failed; retaining deterministic text", "err", err, "intent", rc.intent.ID)
		return
	}
	if text := strings.TrimSpace(res.Text); text != "" {
		rc.resp.Text = text
	}
}

// execTool performs one GET with timeout + token passthrough and logs it.
func (e *Engine) execTool(ctx context.Context, sessionID, token string, t generated.ToolMeta, p string, q url.Values, args json.RawMessage) ([]byte, error) {
	if !IsReadTool(t) {
		return nil, fmt.Errorf("%s is not a read-only tool", t.Name)
	}
	svc, ok := services[t.Service]
	if !ok {
		return nil, fmt.Errorf("unknown service %s", t.Service)
	}
	var headers map[string]string
	if token != "" {
		headers = map[string]string{"X-Token": token}
	}
	tctx, cancel := context.WithTimeout(logx.WithTool(ctx, t.Name), ToolTimeout)
	defer cancel()
	body, err := e.HTTP.Do(tctx, svc, t.Method, p, q, headers, nil) // logged by the client (logx)
	ex := store.ToolExecution{SessionID: sessionID, ToolName: t.Name, Method: t.Method, Path: p, Args: args, Status: "executed"}
	if err != nil {
		ex.Status = "error"
		ex.Result, _ = json.Marshal(map[string]string{"error": err.Error()})
	} else if len(body) <= MaxResultBytes {
		ex.Result = asJSON(body)
	}
	if lerr := e.Store.LogToolExecution(context.WithoutCancel(ctx), ex); lerr != nil {
		log.Printf("store tool execution: %v", lerr)
	}
	return body, err
}

// ---- persistence & analytics ----

func (e *Engine) persist(rc *reqCtx) {
	rc.resp.CreatedAt = time.Now().UTC()
	rc.resp.DurationMs = time.Since(rc.start).Milliseconds()
	turnID := e.logTurn(rc)
	if rc.req.SessionID == "" {
		return
	}
	var meta json.RawMessage
	if turnID != "" || rc.resp.Intent != nil || len(rc.resp.Cards) > 0 || rc.resp.Notice != "" || len(rc.resp.Citations) > 0 || len(rc.resp.Chips) > 0 {
		meta, _ = json.Marshal(map[string]any{
			"turnId":    turnID,
			"intent":    rc.resp.Intent,
			"cards":     rc.resp.Cards,
			"toolCalls": rc.resp.ToolCalls,
			"notice":    rc.resp.Notice,
			"citations": rc.resp.Citations,
			"chips":     rc.resp.Chips,
		})
	}
	err := e.Store.AppendMessages(context.WithoutCancel(rc.ctx), rc.req.SessionID, rc.currentUser().id, rc.pid,
		store.Message{Role: genillm.RoleUser, Content: rc.req.Message, CreatedAt: rc.start.UTC()},
		store.Message{Role: genillm.RoleAssistant, Content: rc.resp.Text, ToolCalls: meta, CreatedAt: rc.resp.CreatedAt, DurationMs: rc.resp.DurationMs},
	)
	if err != nil {
		log.Printf("store append %s: %v", rc.req.SessionID, err)
	}
}

func (e *Engine) logEvent(rc *reqCtx, kind, intent, scenario, message string) {
	ev := store.Event{SessionID: rc.req.SessionID, Kind: kind, Intent: intent, ScenarioID: scenario, PropertyID: rc.pid, Message: message}
	if err := e.Store.LogEvent(context.WithoutCancel(rc.ctx), ev); err != nil {
		log.Printf("store event: %v", err)
	}
}
