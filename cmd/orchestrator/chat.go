package main

import (
	"asi-genie-poc/internal/logx"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	resp, err := a.engine.Handle(ctx, userToken(r), req)
	if err != nil {
		log.Printf("chat: %v", err)
		fail(http.StatusBadGateway, "Genie couldn't answer right now.")
		return
	}
	writeJSON(w, http.StatusOK, resp)
	logChat(r, req, http.StatusOK, resp, "", time.Since(start))
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
	pid, _ := strconv.Atoi(q.Get("propertyId"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	turns, err := a.store.Turns(r.Context(), store.TurnFilter{From: from, To: to, PropertyID: pid, Limit: limit})
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
	if err := a.store.UpdateTurnFeedback(r.Context(), body.TurnID, body.SessionID, feedback); err != nil {
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
	id := r.PathValue("sessionId")
	msgs, err := a.store.History(r.Context(), id, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if msgs == nil {
		msgs = []store.Message{}
	}
	feedbackByTurn := map[string]string{}
	if turns, err := a.store.Turns(r.Context(), store.TurnFilter{SessionID: id, Limit: 500}); err == nil {
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ev, err := a.store.Events(r.Context(), r.URL.Query().Get("kind"), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
