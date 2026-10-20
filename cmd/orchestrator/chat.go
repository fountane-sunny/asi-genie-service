package main

import (
	"asi-genie-poc/internal/logx"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"asi-genie-poc/internal/genie"
	"asi-genie-poc/internal/intents"
	"asi-genie-poc/internal/store"
)

const chatTimeout = 120 * time.Second

// userToken extracts the caller's ASI token: X-Token, then Authorization: Bearer, then the token cookie.
func userToken(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-Token")); t != "" {
		return t
	}
	if a := strings.TrimSpace(r.Header.Get("Authorization")); a != "" {
		if len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
			return strings.TrimSpace(a[7:])
		}
		return a
	}
	if c, err := r.Cookie("token"); err == nil {
		return c.Value
	}
	return ""
}

// requestPropertyID reads the property the caller claims to be asking about.
// Zero means it was omitted. It is never treated as "every property".
func requestPropertyID(r *http.Request) int {
	if v, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("propertyId"))); err == nil && v > 0 {
		return v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.Header.Get("X-Property-Id"))); err == nil && v > 0 {
		return v
	}
	return 0
}

func authFailure(err error) (int, string) {
	switch {
	case errors.Is(err, genie.ErrUnauthenticated):
		return http.StatusUnauthorized, "Sign in to use Genie."
	case errors.Is(err, genie.ErrForbidden):
		return http.StatusForbidden, "You don't have access to this property."
	case errors.Is(err, genie.ErrPropertyRequired):
		return http.StatusBadRequest, "propertyId is required."
	default:
		log.Printf("authorize: %v", err)
		return http.StatusBadGateway, "Genie couldn't verify your sign-in."
	}
}

// gate requires a JWT and a property id, then checks that the token's user
// belongs to that property. ok is false when a response was already written.
func (a *api) gate(w http.ResponseWriter, r *http.Request, propertyID int) (genie.Caller, bool) {
	if userToken(r) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Sign in to use Genie."})
		return genie.Caller{}, false
	}
	if propertyID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "propertyId is required."})
		return genie.Caller{}, false
	}
	caller, err := a.engine.Authorize(r.Context(), userToken(r), propertyID)
	if err != nil {
		status, msg := authFailure(err)
		writeJSON(w, status, map[string]string{"error": msg})
		return genie.Caller{}, false
	}
	return caller, true
}

type api struct {
	engine *genie.Engine
	store  store.Store
}

// POST /chat
func (a *api) chat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req genie.Request
	fail := func(status int, msg string) {
		writeJSON(w, status, map[string]string{"error": msg})
		logChat(r, req, status, nil, msg, time.Since(start))
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		fail(http.StatusBadRequest, "message is required")
		return
	}
	if req.PropertyID == 0 {
		if v, err := strconv.Atoi(r.Header.Get("X-Property-Id")); err == nil {
			req.PropertyID = v
		}
	}
	if userToken(r) == "" {
		fail(http.StatusUnauthorized, "Sign in to use Genie.")
		return
	}
	// Genie is property-specific: no group chat, no chat without a property.
	if req.GroupID != 0 || r.Header.Get("X-Group-Id") != "" {
		fail(http.StatusBadRequest, "Genie works per property. Open a property to chat - group chat isn't supported.")
		return
	}
	if req.PropertyID <= 0 {
		fail(http.StatusBadRequest, "propertyId is required. Open a property to chat with Genie.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	if _, err := a.engine.Authorize(ctx, userToken(r), req.PropertyID); err != nil {
		status, msg := authFailure(err)
		fail(status, msg)
		return
	}
	resp, err := a.engine.Handle(ctx, userToken(r), req)
	if err != nil {
		log.Printf("chat: %v", err)
		fail(http.StatusBadGateway, "Genie couldn't answer right now.")
		return
	}
	writeJSON(w, http.StatusOK, resp)
	logChat(r, req, http.StatusOK, resp, "", time.Since(start))
}

// POST /chat/stream uses the same authorization and engine path as /chat,
// then serializes the completed deterministic result as SSE UI events.
func (a *api) chatStream(w http.ResponseWriter, r *http.Request) {
	var req genie.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeSSEError(w, http.StatusBadRequest, "message is required")
		return
	}
	if req.PropertyID == 0 {
		req.PropertyID = requestPropertyID(r)
	}
	if userToken(r) == "" {
		writeSSEError(w, http.StatusUnauthorized, "Sign in to use Genie.")
		return
	}
	if req.GroupID != 0 || r.Header.Get("X-Group-Id") != "" || req.PropertyID <= 0 {
		writeSSEError(w, http.StatusBadRequest, "propertyId is required.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	if _, err := a.engine.Authorize(ctx, userToken(r), req.PropertyID); err != nil {
		status, message := authFailure(err)
		writeSSEError(w, status, message)
		return
	}
	resp, err := a.engine.Handle(ctx, userToken(r), req)
	if err != nil {
		slog.Error("stream chat failed", "err", err)
		writeSSEError(w, http.StatusBadGateway, "Genie couldn't answer right now.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Error("stream response writer does not support flushing")
		return
	}
	for i, call := range resp.ToolCalls {
		id := fmt.Sprintf("tool-%d", i+1)
		writeSSE(w, "tool_start", map[string]any{"id": id, "name": call.Name, "label": toolLabel(call.Name)})
		writeSSE(w, "tool_end", map[string]any{"id": id, "status": call.Status, "ms": call.DurationMs})
	}
	writeSSE(w, "text_delta", map[string]string{"text": resp.Text})
	for _, card := range resp.Cards {
		writeSSE(w, "card", card)
	}
	if len(resp.Chips) > 0 {
		writeSSE(w, "chips", resp.Chips)
	}
	writeSSE(w, "done", map[string]string{"turnId": resp.TurnID, "sessionId": resp.SessionID})
	flusher.Flush()
}

func toolLabel(name string) string { return genie.ToolLabel(name) }

func writeSSE(w http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("SSE event encoding failed", "err", err, "event", event)
		return
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		slog.Warn("SSE event write failed", "err", err, "event", event)
	}
}

func writeSSEError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(status)
	writeSSE(w, "error", map[string]string{"error": message})
}

// logChat prints the "── CHAT ▶ POST /chat" block.
func logChat(r *http.Request, req genie.Request, status int, resp *genie.Response, errMsg string, d time.Duration) {
	msg, _ := logx.MaskPAN(req.Message)
	fields := []logx.Field{
		logx.F("session", req.SessionID),
		logx.F("property", req.PropertyID),
		logx.F("token", map[bool]string{true: "present", false: "missing"}[userToken(r) != ""]),
		logx.F("message", logx.Trunc(msg, 500)),
	}
	if errMsg != "" {
		fields = append(fields, logx.F("error", errMsg))
	}
	if resp != nil {
		if it := resp.Intent; it != nil {
			fields = append(fields, logx.F("intent", fmt.Sprintf("%s (%s, %s, conf %.2f)", it.ID, it.Source, it.Handler, it.Confidence)))
		}
		var cs []string
		for _, c := range resp.Cards {
			t := string(c.Type)
			if c.Title != "" {
				t += " \"" + c.Title + "\""
			}
			cs = append(cs, t)
		}
		fields = append(fields,
			logx.F("cards", strings.Join(cs, ", ")),
			logx.F("reply", logx.Trunc(resp.Text, 300)),
			logx.B("response", resp),
		)
	}
	logx.Block("CHAT", r.Method+" "+r.URL.Path, fmt.Sprintf("HTTP %d", status), d, status < 400, fields...)
}

// GET /analytics/turns?from=2026-10-01&to=2026-10-13&propertyId=12&limit=1000&format=csv
// from/to: RFC 3339 or YYYY-MM-DD (to is exclusive). JSON by default, CSV with format=csv.
func (a *api) turns(w http.ResponseWriter, r *http.Request) {
	pid := requestPropertyID(r)
	caller, ok := a.gate(w, r, pid)
	if !ok {
		return
	}
	q := r.URL.Query()
	parse := func(k string) (time.Time, error) {
		v := q.Get(k)
		if v == "" {
			return time.Time{}, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
		return time.Parse("2006-01-02", v)
	}
	from, err1 := parse("from")
	to, err2 := parse("to")
	if err1 != nil || err2 != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from/to must be YYYY-MM-DD or RFC 3339"})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	turns, err := a.store.Turns(r.Context(), store.TurnFilter{From: from, To: to, PropertyID: pid, UserID: caller.UserID, Limit: limit})
	if err != nil {
		log.Printf("analytics: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "couldn't read analytics"})
		return
	}
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="genie-turns.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"createdAt", "sessionId", "propertyId", "userId", "userName", "userEmail", "message", "reply", "intent", "scenarioId", "source", "confidence", "slots", "tools", "cards", "llmModel", "llmFailed", "durationMs", "errorCode", "errorMessage", "feedback"})
		for _, t := range turns {
			fb := ""
			if t.Feedback != nil {
				fb = *t.Feedback
			}
			_ = cw.Write([]string{t.CreatedAt.Format(time.RFC3339), t.SessionID, strconv.Itoa(t.PropertyID), t.UserID, t.UserName, t.UserEmail,
				t.Message, t.Reply, t.Intent, t.ScenarioID, t.Source, strconv.FormatFloat(t.Confidence, 'f', 2, 64), string(t.Slots), string(t.Tools),
				string(t.Cards), t.LLMModel, strconv.FormatBool(t.LLMFailed), strconv.FormatInt(t.DurationMs, 10), t.ErrorCode, t.ErrorMessage, fb})
		}
		cw.Flush()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(turns), "turns": turns})
}

type feedbackRequest struct {
	TurnID    string `json:"turnId"`
	SessionID string `json:"sessionId"`
	Rating    string `json:"rating"`
	Comment   string `json:"comment,omitempty"`
}

// POST /feedback — persist thumbs up/down on a chat turn (genie_turns.feedback).
func (a *api) feedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	pid := requestPropertyID(r)
	caller, ok := a.gate(w, r, pid)
	if !ok {
		return
	}
	var body feedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	body.TurnID = strings.TrimSpace(body.TurnID)
	body.SessionID = strings.TrimSpace(body.SessionID)
	body.Rating = strings.TrimSpace(body.Rating)
	if body.TurnID == "" || body.Rating == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "turnId and rating are required"})
		return
	}
	feedback := body.Rating
	if c := strings.TrimSpace(body.Comment); c != "" {
		feedback = body.Rating + ": " + c
	}
	switch body.Rating {
	case "thumbs_up", "thumbs_down":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rating must be thumbs_up or thumbs_down"})
		return
	}
	if err := a.store.UpdateTurnFeedback(r.Context(), body.TurnID, body.SessionID, caller.UserID, pid, feedback); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "turn not found"})
			return
		}
		log.Printf("feedback: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save feedback"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /conversations/{sessionId}
func (a *api) history(w http.ResponseWriter, r *http.Request) {
	pid := requestPropertyID(r)
	caller, ok := a.gate(w, r, pid)
	if !ok {
		return
	}
	id := r.PathValue("sessionId")
	msgs, err := a.store.History(r.Context(), id, caller.UserID, pid, 0)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "conversation not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "couldn't read conversation"})
		return
	}
	if msgs == nil {
		msgs = []store.Message{}
	}
	feedbackByTurn := map[string]string{}
	if turns, err := a.store.Turns(r.Context(), store.TurnFilter{SessionID: id, PropertyID: pid, UserID: caller.UserID, Limit: 500}); err == nil {
		for _, t := range turns {
			if t.Feedback != nil && *t.Feedback != "" {
				feedbackByTurn[t.ID] = *t.Feedback
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": id, "messages": msgs, "feedbackByTurnId": feedbackByTurn})
}

// GET /intents - the Phase 1 capability registry (from the scenario catalogue).
func (a *api) listIntents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"count": len(intents.All()), "intents": intents.All()})
}

// GET /events?kind=unsupported&limit=100 - captured requests / denials (redacted analytics).
func (a *api) events(w http.ResponseWriter, r *http.Request) {
	pid := requestPropertyID(r)
	caller, ok := a.gate(w, r, pid)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ev, err := a.store.Events(r.Context(), r.URL.Query().Get("kind"), caller.UserID, pid, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "couldn't read events"})
		return
	}
	if ev == nil {
		ev = []store.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev})
}

// withCORS allows the local Next.js dev server (GENIE_CORS_ORIGIN overrides).
func withCORS(next http.Handler) http.Handler {
	origin := os.Getenv("GENIE_CORS_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3000"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Token, X-Browser-Name, X-Property-Id")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		switch {
		case r.URL.Path == "/health" || r.URL.Path == "/v1/genie/healthcheck" || r.Method == http.MethodOptions:
			// quiet
		case r.URL.Path == "/chat":
			// logged as a block by logChat
		default:
			logx.Line("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
		}
	})
}
