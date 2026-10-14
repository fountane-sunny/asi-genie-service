package genie

import (
	"errors"
	"fmt"
	"strings"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

// Friendly, user-facing error text. Raw errors only go to the logs.

var serviceLabel = map[string]string{
	"reservation": "reservation", "config": "property settings", "auth": "sign-in", "reporting": "reporting",
}

// what describes an intent for messages ("today's arrivals").
func what(it intents.Intent) string {
	s := strings.ReplaceAll(it.ID, "_", " ")
	switch {
	case s == "":
		return "that"
	case strings.HasPrefix(s, "todays "):
		return "today's " + strings.TrimPrefix(s, "todays ")
	}
	return s
}

// errorCode classifies a tool/handler error.
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "http 401"):
		return "session_expired"
	case strings.Contains(m, "http 403"):
		return "forbidden"
	case strings.Contains(m, "http 404"):
		return "not_found"
	case errors.Is(err, errNoTool):
		return "not_available"
	case strings.Contains(m, "http 5"):
		return "service_error"
	case strings.Contains(m, "deadline"), strings.Contains(m, "timeout"), strings.Contains(m, "connection refused"),
		strings.Contains(m, "no such host"), strings.Contains(m, "base url"), strings.Contains(m, "eof"), strings.Contains(m, "reset by peer"):
		return "service_unavailable"
	}
	return "unexpected"
}

// errorCard turns err into a readable card + one-line reply.
func errorCard(it intents.Intent, err error, service string, link *cards.Link) (cards.Card, string) {
	w := what(it)
	svc := serviceLabel[service]
	if svc == "" {
		svc = "ASI"
	}
	code := errorCode(err)
	var title, msg, retry string
	switch code {
	case "session_expired":
		title, msg, retry = "Your session has expired", "ASI didn't accept your sign-in, so I couldn't load "+w+".", "Please log in again, then ask me again."
	case "forbidden":
		title, msg, retry = "You don't have access to "+w, "Your role doesn't include access to "+w+" for this property.", "Ask a manager or admin if you need access."
	case "not_found":
		title, msg, retry = "Couldn't find "+w, "The "+svc+" service has no data for that.", "Check the details (date, room, confirmation number) and try again."
	case "service_unavailable":
		title, msg, retry = "Couldn't load "+w, fmt.Sprintf("Couldn't load %s - the %s service didn't respond.", w, svc), "Please try again in a moment."
	case "service_error":
		title, msg, retry = "Couldn't load "+w, fmt.Sprintf("The %s service had a problem loading %s.", svc, w), "Please try again in a moment, or open the screen directly."
	case "not_available":
		title, msg, retry = "Can't fetch "+w+" yet", "I don't have a way to read "+w+" yet.", "Open the screen directly."
	default:
		title, msg, retry = "Couldn't load "+w, "Something went wrong while loading "+w+".", "Please try again, or open the screen directly."
	}
	return cards.Error(it.ID, code, title, msg, retry, link), msg + " " + retry
}

// llmBusyNote is added when the classifier LLM failed and keywords were used.
const llmBusyNote = "The AI is busy right now, so I used quick matching - the answer may be less precise."
