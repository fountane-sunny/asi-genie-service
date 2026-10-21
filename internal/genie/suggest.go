package genie

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"asi-genie-poc/internal/cards"
	genillm "asi-genie-poc/llm"
)

const (
	suggestTool       = "suggest_next"
	maxSuggestions    = 3
	maxSuggestionSize = 160
)

// Message is the next user turn, so it must be a request Genie can carry out.
type suggestedAction struct {
	Label   string `json:"label"`
	Message string `json:"message"`
}

type suggestedNext struct {
	Suggestions []suggestedAction `json:"suggestions"`
}

const suggestSystem = `You suggest what the user can ask ASI Genie to do next, by calling suggest_next exactly once.
Each suggestion is shown as a chip and, when tapped, is sent as their next message. Genie will perform that message. Only suggest an action Genie can complete from this reply:
- show property data (arrivals, departures, in-house, occupancy, a guest, a reservation, a balance, a report)
- email, print, or download a folio document for a guest or reservation already named in the reply (folio, detailed folio, registration form, other charges)
Copy guest names and reservation numbers from the reply. Do not invent them.
Do not suggest payments, refunds, check-in, check-out, or creating or cancelling a reservation.
If the reply says a folio was already sent, do not suggest sending it again. Suggest downloading or printing it, or showing the balance.
Return 2 or 3 suggestions. label is the short chip. message is the full request, at most 160 characters, and must stand alone without the previous question.
If nothing useful follows from this reply, return an empty list.`

// suggestionsEnabled is GENIE_SUGGESTIONS. It stays off unless set to true:
// follow-up chips are a separate model call, and the answer prompt asks for
// them too.
func suggestionsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GENIE_SUGGESTIONS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (rc *reqCtx) attachSuggestions() {
	if !suggestionsEnabled() || len(rc.resp.Chips) > 0 || rc.hasChoiceCard() || rc.e == nil || rc.e.LLM == nil || rc.llmFailed {
		return
	}
	if strings.TrimSpace(rc.resp.Text) == "" && len(rc.resp.Cards) == 0 {
		return
	}
	ctx, cancel := rc.llmCtx()
	res, err := rc.e.LLM.SendMessage(ctx, suggestSystem, []genillm.Message{{
		Role: genillm.RoleUser, Content: rc.suggestionContext(),
	}}, []genillm.Tool{suggestToolDef()})
	cancel()
	if err != nil {
		slog.Warn("genie suggestions failed", "err", err, "propertyId", rc.pid)
		return
	}
	for _, tc := range res.ToolCalls {
		if tc.Name != suggestTool {
			continue
		}
		var next suggestedNext
		if err := json.Unmarshal(tc.Arguments, &next); err != nil {
			slog.Warn("genie suggestions were invalid", "err", err, "propertyId", rc.pid)
			return
		}
		rc.applySuggestions(next.Suggestions)
		return
	}
}

func (rc *reqCtx) hasChoiceCard() bool {
	for _, c := range rc.resp.Cards {
		if c.Type == cards.TypeClarify || c.Type == cards.TypeDisambiguation {
			return true
		}
	}
	return false
}

func (rc *reqCtx) suggestionContext() string {
	var b strings.Builder
	fmt.Fprintf(&b, "User: %s\n", strings.TrimSpace(rc.req.Message))
	if text := strings.TrimSpace(rc.resp.Text); text != "" {
		if len(text) > 800 {
			text = text[:800]
		}
		fmt.Fprintf(&b, "Reply: %s\n", text)
	}
	for _, c := range rc.resp.Cards {
		if title := strings.TrimSpace(c.Title); title != "" {
			fmt.Fprintf(&b, "Card: %s\n", title)
		}
		if msg := strings.TrimSpace(c.Message); msg != "" && msg != c.Title {
			fmt.Fprintf(&b, "Card detail: %s\n", msg)
		}
	}
	return b.String()
}

func (rc *reqCtx) applySuggestions(items []suggestedAction) {
	seen := map[string]bool{}
	asked := strings.ToLower(strings.TrimSpace(rc.req.Message))
	for _, item := range items {
		if len(rc.resp.Chips) >= maxSuggestions {
			return
		}
		message := strings.TrimSpace(item.Message)
		label := strings.TrimSpace(item.Label)
		if message == "" {
			message = label
		}
		if label == "" {
			label = message
		}
		if message == "" || len(message) > maxSuggestionSize || len(label) > maxSuggestionSize {
			continue
		}
		key := strings.ToLower(message)
		if seen[key] || key == asked {
			continue
		}
		if !suggestionAllowed(message) {
			continue
		}
		seen[key] = true
		rc.resp.Chips = append(rc.resp.Chips, cards.Chip{Label: label, Message: message})
	}
}

// Folio email and download are allowed; other writes are not.
func suggestionAllowed(message string) bool {
	if messageWantsFolioAction(message) {
		return true
	}
	return !isReadOnlyRefusal(message)
}

func suggestToolDef() genillm.Tool {
	str := map[string]any{"type": "string"}
	return genillm.Tool{
		Name:        suggestTool,
		Description: "Suggest the next requests the user can send. Each one is performed when they tap it.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []string{"suggestions"},
			"properties": map[string]any{
				"suggestions": map[string]any{
					"type":     "array",
					"maxItems": maxSuggestions,
					"items": map[string]any{
						"type":     "object",
						"required": []string{"label", "message"},
						"properties": map[string]any{
							"label":   map[string]any{"type": "string", "description": "short chip text"},
							"message": str,
						},
					},
				},
			},
		},
	}
}
