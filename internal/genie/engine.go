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

type Request struct {
	SessionID     string            `json:"sessionId,omitempty"`
	Message       string            `json:"message"`
	GroupID       int               `json:"groupId,omitempty"` // rejected: no group chat
	PropertyID    int               `json:"propertyId,omitempty"`
	BusinessDate  string            `json:"businessDate,omitempty"`
	LocalHour     *int              `json:"localHour,omitempty"`
	Screen        string            `json:"screen,omitempty"`
	History       []genillm.Message `json:"history,omitempty"`
	ReplaceTurnID string            `json:"replaceTurnId,omitempty"`
}

type IntentInfo struct {
	ID           string  `json:"id"`
	ScenarioID   string  `json:"scenarioId,omitempty"`
	ResponseType string  `json:"responseType,omitempty"`
	Handler      string  `json:"handler"`
	Confidence   float64 `json:"confidence,omitempty"`
	Source       string  `json:"source"`
	Slots        Slots   `json:"slots,omitempty"`
}

type ToolCallStatus struct {
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args"`
	Status     string          `json:"status"`
	Error      string          `json:"error,omitempty"` // error code only; raw errors stay in the logs
	DurationMs int64           `json:"durationMs"`
}

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
	Notice       string           `json:"notice,omitempty"`
	CreatedAt    time.Time        `json:"createdAt"`
	DurationMs   int64            `json:"durationMs"`
	TurnID       string           `json:"turnId,omitempty"`
}

type Entity struct {
	Kind  string `json:"kind"`
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

type Engine struct {
	LLM      genillm.LLMClient
	HTTP     *generated.Client
	Tools    map[string]generated.ToolMeta // GET reads only; writes are not visible to the model
	Selector *ToolSelector
	Store    store.Store

	mu       sync.Mutex
	sessions map[string]*session
	users    map[string]userInfo
	Model    string
	bizDates map[int]dateEntry
	handlers map[string]handlerFunc
}

type dateEntry struct {
	date string
	at   time.Time
}

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

type reqCtx struct {
	mu             sync.Mutex
	llmDeadline    time.Time
	llmFailed      bool // don't call the LLM again after it failed this request
	start          time.Time
	lastErrService string
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

// A session is bound to one property; a property change starts fresh.
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

func (rc *reqCtx) currentUser() userInfo {
	u, _ := rc.loadCaller()
	return u
}

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
	if rc.req.ReplaceTurnID != "" {
		err := e.Store.UpdateTurn(context.WithoutCancel(rc.ctx), rc.req.ReplaceTurnID, t)
		if err == nil {
			rc.resp.TurnID = rc.req.ReplaceTurnID
			return rc.req.ReplaceTurnID
		}
		slog.Warn("retry kept a new turn", "err", err, "turnId", rc.req.ReplaceTurnID, "propertyId", rc.pid)
	}
	id, err := e.Store.LogTurn(context.WithoutCancel(rc.ctx), t)
	if err != nil {
		slog.Error("failed to store turn", "err", err, "propertyId", rc.pid)
		return ""
	}
	rc.resp.TurnID = id
	return id
}

func maskPAN(s string) (string, bool) { return logx.MaskPAN(s) }

var ErrPropertyRequired = errors.New("propertyId is required: Genie is property-specific (group chat isn't supported)")

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

	// Never send card numbers to the model or the store.
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

	if req.PropertyID <= 0 {
		return nil, ErrPropertyRequired
	}
	rc.pid = req.PropertyID
	rc.resp.PropertyID = rc.pid
	// Keyword refusals only when no model is configured; otherwise the planner writes the reply.
	if e.LLM == nil && isReadOnlyRefusal(rc.req.Message) {
		rc.setIntent(IntentInfo{ID: intents.UnsupportedID, Handler: "fallback", Source: "rule"})
		rc.add(cards.NotSupported(intents.UnsupportedID, "", nil))
		rc.text("I only help with this property in ASI — arrivals, rooms, rates, and reports. I can't answer that.")
		e.persist(rc)
		return rc.resp, nil
	}
	// "today" is the night-audit working date, not the server clock.
	rc.bizDate = e.businessDate(rc)
	rc.resp.BusinessDate = rc.bizDate

	if e.LLM != nil && !rc.routerOwnsTurn() && e.planAndAnswer(rc) {
		rc.attachSuggestions()
		e.mu.Lock()
		sess.LastIntent = rc.resp.Intent.ID
		e.mu.Unlock()
		e.logEvent(rc, "intent", rc.resp.Intent.ID, "", "")
		e.persist(rc)
		return rc.resp, nil
	}

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
	// print/download/email folio must not stay on the folio_document navigate guide.
	it, info, slots = e.applyFolioIntent(it, info, slots, rc.req.Message)
	rc.intent = it
	info.Slots = slots
	rc.setIntent(info)
	rc.resp.Intent.Slots = slots

	if err := e.dispatch(rc, it, slots, info); err != nil {
		// Raw errors stay in the logs; the client gets an error card.
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
	rc.attachSuggestions()
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

var LLMTimeout = envDuration("LLM_TIMEOUT", 30*time.Second)

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil && d > 0 {
		return d
	}
	return def
}

func (rc *reqCtx) llmCtx() (context.Context, context.CancelFunc) {
	if rc.llmDeadline.IsZero() {
		rc.llmDeadline = time.Now().Add(LLMTimeout)
	}
	return context.WithDeadline(rc.ctx, rc.llmDeadline)
}

var errNoTool = errors.New("no read endpoint available")

func isAuthErr(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "HTTP 401") || strings.Contains(m, "HTTP 403")
}

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

func (rc *reqCtx) phraseResponse() {
	// Keep a sentence the handler already wrote so the card text is not repeated.
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
	body, err := e.HTTP.Do(tctx, svc, t.Method, p, q, headers, nil)
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

func findTool(name string) (generated.ToolMeta, bool) {
	for _, t := range generated.AllTools() {
		if t.Name == name {
			return t, true
		}
	}
	return generated.ToolMeta{}, false
}

func (e *Engine) execWriteTool(ctx context.Context, sessionID, token string, t generated.ToolMeta, p string, body any, args json.RawMessage) ([]byte, error) {
	if !IsAllowedWriteTool(t) {
		return nil, fmt.Errorf("%s is not an allowed write tool", t.Name)
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
	respBody, err := e.HTTP.Do(tctx, svc, t.Method, p, nil, headers, body)
	ex := store.ToolExecution{SessionID: sessionID, ToolName: t.Name, Method: t.Method, Path: p, Args: args, Status: "executed"}
	if err != nil {
		ex.Status = "error"
		ex.Result, _ = json.Marshal(map[string]string{"error": err.Error()})
	} else if len(respBody) <= MaxResultBytes {
		ex.Result = asJSON(respBody)
	}
	if lerr := e.Store.LogToolExecution(context.WithoutCancel(ctx), ex); lerr != nil {
		log.Printf("store tool execution: %v", lerr)
	}
	return respBody, err
}

func (rc *reqCtx) post(toolName string, pathParams map[string]string, body any) (any, error) {
	t, ok := findTool(toolName)
	if !ok || !IsAllowedWriteTool(t) {
		return nil, fmt.Errorf("%s: %w", toolName, errNoTool)
	}
	p := t.Path
	for k, v := range pathParams {
		p = strings.ReplaceAll(p, "{"+k+"}", url.PathEscape(v))
	}
	if strings.Contains(p, "{") {
		return nil, fmt.Errorf("%s: missing path parameter in %s", toolName, p)
	}
	argsJSON, _ := json.Marshal(map[string]any{"path": pathParams})
	st := ToolCallStatus{Name: toolName, Args: argsJSON}
	t0 := time.Now()
	b, err := rc.e.execWriteTool(rc.ctx, rc.req.SessionID, rc.token, t, p, body, argsJSON)
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
	if err != nil {
		return nil, err
	}
	return decode(b), nil
}

func (e *Engine) persist(rc *reqCtx) {
	rc.resp.CreatedAt = time.Now().UTC()
	rc.resp.DurationMs = time.Since(rc.start).Milliseconds()
	turnID := e.logTurn(rc)
	if rc.req.SessionID == "" {
		return
	}
	var meta json.RawMessage
	if turnID != "" || rc.resp.Intent != nil || len(rc.resp.Cards) > 0 || rc.resp.Notice != "" || len(rc.resp.Citations) > 0 || len(rc.resp.Chips) > 0 {
		saved := map[string]any{
			"turnId":    turnID,
			"intent":    rc.resp.Intent,
			"cards":     rc.resp.Cards,
			"toolCalls": rc.resp.ToolCalls,
			"notice":    rc.resp.Notice,
			"citations": rc.resp.Citations,
			"chips":     rc.resp.Chips,
		}
		// The confirm payload has to survive a new server process.
		if ent := rc.sess.LastEntity; ent != nil && (ent.Kind == emailFolioPendingKind || ent.Kind == emailFolioSentKind) {
			saved["pendingEmail"] = ent
		}
		meta, _ = json.Marshal(saved)
	}
	userMsg := store.Message{Role: genillm.RoleUser, Content: rc.req.Message, CreatedAt: rc.start.UTC()}
	assistantMsg := store.Message{Role: genillm.RoleAssistant, Content: rc.resp.Text, ToolCalls: meta, CreatedAt: rc.resp.CreatedAt, DurationMs: rc.resp.DurationMs}
	if rc.req.ReplaceTurnID != "" {
		replaced, err := e.Store.ReplaceTurnMessages(context.WithoutCancel(rc.ctx), rc.req.SessionID, rc.currentUser().id, rc.pid, rc.req.ReplaceTurnID, userMsg, assistantMsg)
		if err != nil {
			slog.Error("failed to replace retried messages", "err", err, "turnId", rc.req.ReplaceTurnID, "propertyId", rc.pid)
		}
		if replaced {
			return
		}
	}
	err := e.Store.AppendMessages(context.WithoutCancel(rc.ctx), rc.req.SessionID, rc.currentUser().id, rc.pid, userMsg, assistantMsg)
	if err != nil {
		slog.Error("failed to append messages", "err", err, "sessionId", rc.req.SessionID, "propertyId", rc.pid)
	}
}

func (e *Engine) logEvent(rc *reqCtx, kind, intent, scenario, message string) {
	ev := store.Event{SessionID: rc.req.SessionID, Kind: kind, Intent: intent, ScenarioID: scenario, PropertyID: rc.pid, Message: message}
	if err := e.Store.LogEvent(context.WithoutCancel(rc.ctx), ev); err != nil {
		log.Printf("store event: %v", err)
	}
}
