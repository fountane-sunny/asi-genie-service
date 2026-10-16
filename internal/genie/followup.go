package genie

import (
	"encoding/json"
	"strings"

	"asi-genie-poc/internal/intents"
	genillm "asi-genie-poc/llm"
)

// resolveFollowUpIntent treats the user's message as the missing slot when the
// previous turn ended in a clarify/disambiguation for the same capability.
func (e *Engine) resolveFollowUpIntent(rc *reqCtx, it intents.Intent, info IntentInfo, slots Slots) (intents.Intent, IntentInfo, Slots) {
	pending := rc.sess.LastIntent
	if pending == "" {
		pending = rc.pendingClarifyIntentFromHistory()
	}
	if pending == "" {
		return it, info, slots
	}
	msg := strings.TrimSpace(rc.req.Message)
	if msg == "" || looksLikeNewQuestion(msg) {
		return it, info, slots
	}
	if !fillFollowUpSlot(pending, msg, &slots) {
		return it, info, slots
	}
	it2, ok := intents.Get(pending)
	if !ok {
		return it, info, slots
	}
	info = IntentInfo{
		ID:           it2.ID,
		ScenarioID:   it2.ScenarioID,
		ResponseType: it2.ResponseType,
		Handler:      "dedicated",
		Source:       "rule",
		Confidence:   1,
		Slots:        slots,
	}
	return it2, info, slots
}

func fillFollowUpSlot(pendingIntent, message string, s *Slots) bool {
	switch pendingIntent {
	case "lookup_guest":
		if s.GuestName != "" {
			return false
		}
		s.GuestName = message
		return true
	case "lookup_reservation":
		if s.GuestName != "" || s.Confirmation != "" || s.Room != "" {
			return false
		}
		s.GuestName = message
		return true
	case "view_report":
		if strings.TrimSpace(s.Report) != "" {
			return false
		}
		s.Report = message
		return true
	default:
		return false
	}
}

func looksLikeNewQuestion(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return true
	}
	if strings.Contains(lower, "?") {
		return true
	}
	prefixes := []string{
		"who ", "what ", "when ", "where ", "which ", "how ",
		"show ", "open ", "go to ", "give me", "give ",
		"look up", "lookup ", "find ", "search ",
		"can you", "could you", "please ", "tell me",
		"night audit", "arrival", "departure", "report",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func (rc *reqCtx) pendingClarifyIntentFromHistory() string {
	if rc.req.SessionID == "" {
		return ""
	}
	u := rc.currentUser()
	msgs, err := rc.e.Store.History(rc.ctx, rc.req.SessionID, u.id, rc.pid, 6)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != genillm.RoleAssistant {
			continue
		}
		if intent := clarifyIntentFromPersistMeta(msgs[i].ToolCalls); intent != "" {
			return intent
		}
		break
	}
	return ""
}

func clarifyIntentFromPersistMeta(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var meta struct {
		Cards []struct {
			Type   string `json:"type"`
			Intent string `json:"intent"`
		} `json:"cards"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return ""
	}
	for _, c := range meta.Cards {
		if c.Type == "clarify" || c.Type == "disambiguation" {
			if c.Intent != "" {
				return c.Intent
			}
		}
	}
	return ""
}
