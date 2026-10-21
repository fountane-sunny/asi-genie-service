package genie

import (
	"encoding/json"
	"strings"

	"asi-genie-poc/internal/intents"
	genillm "asi-genie-poc/llm"
)

func (e *Engine) resolveFollowUpIntent(rc *reqCtx, it intents.Intent, info IntentInfo, slots Slots) (intents.Intent, IntentInfo, Slots) {
	pending := rc.sess.LastIntent
	if pending == "" {
		pending = rc.pendingClarifyIntentFromHistory()
	}
	if pending == "" {
		return it, info, slots
	}
	msg := strings.TrimSpace(rc.req.Message)
	// A status question after a sent folio stays on email_folio so the recorded result is used.
	if pending == "email_folio" {
		if _, ok := decodeEmailFolioSent(rc.sess.LastEntity); ok && messageAsksFolioEmailStatus(msg) {
			it2, found := intents.Get(pending)
			if !found {
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
	}
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

// A bare "print it" has no guest name; using it as a search slot would look up those words.
func (rc *reqCtx) isBareFolioPrintFollowUp() bool {
	if rc.sess.LastIntent != "download_folio" || messageWantsFolioAction(rc.req.Message) {
		return false
	}
	lower := strings.ToLower(rc.req.Message)
	return strings.Contains(lower, "print") || strings.Contains(lower, "download")
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
	case "download_report":
		return true
	case "download_folio":
		if strings.Contains(strings.ToLower(message), "print") || strings.Contains(strings.ToLower(message), "download") {
			return true
		}
		if s.GuestName != "" || s.Confirmation != "" || s.Room != "" {
			return false
		}
		s.GuestName = message
		return true
	case "email_folio":
		// Confirm replies and "which reservation?" both continue; LastEntity tells them apart.
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
		"download ", "export ", "check ", "list ", "compare ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func shouldSendConversationContext(message string) bool {
	if strings.TrimSpace(message) == "" {
		return false
	}
	if refersToPriorContext(message) {
		return true
	}
	return !looksLikeNewQuestion(message)
}

func refersToPriorContext(message string) bool {
	lower := strings.ToLower(message)
	phrases := []string{
		"that booking", "that reservation", "that guest", "that room", "that one",
		"this guest", "this booking", "this room", "this one",
		"same room", "same guest", "same booking", "same one",
	}
	for _, p := range phrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	pronouns := map[string]bool{
		"her": true, "hers": true, "his": true, "him": true,
		"she": true, "he": true, "they": true, "them": true, "their": true,
	}
	for _, w := range strings.Fields(lower) {
		w = strings.Trim(w, "?.!,;:'\"")
		if pronouns[w] {
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
