// Package cards defines the typed Phase 1 Genie response contract.
// Every reply is one or more cards; see docs/response-contract.md.
package cards

import (
	"fmt"
	"strings"
)

// Card types (one per catalogue Phase 1 response type).
const (
	TypeText           = "text"           // Text answer
	TypeData           = "data"           // Data card
	TypeList           = "list"           // List card
	TypeComparison     = "comparison"     // Comparison card
	TypeChecklist      = "checklist"      // Checklist / readiness card
	TypeNavigate       = "navigate"       // Navigate (deep link)
	TypeGuide          = "guide"          // Guide + citation
	TypeClarify        = "clarify"        // Clarifying question
	TypeDisambiguation = "disambiguation" // Disambiguation
	TypeError          = "error"          // readable failure: title, message, retry hint (never raw errors)
	TypeDenied         = "denied"         // Denied (RBAC)
	TypeNotSupported   = "not_supported"  // Not supported (captured)
)

// Link is a frontend deep link. Route is an asi-frontend path; Params become query params.
type Link struct {
	Label  string            `json:"label"`
	Route  string            `json:"route"`
	Params map[string]string `json:"params,omitempty"`
}

// Field is an ordered label/value pair.
type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Hint  string `json:"hint,omitempty"` // e.g. "+4 vs yesterday"
}

// ListItem is one row in a list card or disambiguation pick-list.
type ListItem struct {
	ID       string   `json:"id,omitempty"`
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Badges   []string `json:"badges,omitempty"`
	Link     *Link    `json:"link,omitempty"`
}

// Period is one side of a comparison.
type Period struct {
	Label   string   `json:"label"`
	Value   *float64 `json:"value,omitempty"`
	Display string   `json:"display"`
}

// Comparison holds two periods and server-computed deltas.
type Comparison struct {
	Metric   string   `json:"metric"`
	Current  Period   `json:"current"`
	Previous Period   `json:"previous"`
	Delta    *float64 `json:"delta,omitempty"`
	DeltaPct *float64 `json:"deltaPct,omitempty"`
}

// Check statuses.
const (
	CheckOK      = "ok"
	CheckWarn    = "warn"
	CheckFail    = "fail"
	CheckUnknown = "unknown" // could not be read (missing permission or API error)
)

// CheckItem is one checklist/readiness row.
type CheckItem struct {
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Link   *Link  `json:"link,omitempty"`
}

// Option is a clarifying-question / disambiguation choice. Sending Message back
// as the next chat message (or Value as the slot) resolves the question.
type Option struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Message string `json:"message"`
}

// Citation names the help/policy source of a guide answer.
type Citation struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Snippet string `json:"snippet,omitempty"`
}

// Card is the single typed card shape. Only the fields relevant to Type are set.
//
// Fields (map) is kept for backwards compatibility with the first chat panel,
// which renders `fields` as key/value pairs; new clients should use Rows/Items/etc.
type Card struct {
	Type       string            `json:"type"`
	Intent     string            `json:"intent,omitempty"`
	Title      string            `json:"title"`
	Subtitle   string            `json:"subtitle,omitempty"`
	Message    string            `json:"message,omitempty"`
	Rows       []Field           `json:"rows,omitempty"`
	Items      []ListItem        `json:"items,omitempty"`
	Total      int               `json:"total,omitempty"`
	Comparison *Comparison       `json:"comparison,omitempty"`
	Checklist  []CheckItem       `json:"checklist,omitempty"`
	Options    []Option          `json:"options,omitempty"`
	Steps      []string          `json:"steps,omitempty"`
	Citations  []Citation        `json:"citations,omitempty"`
	Link       *Link             `json:"link,omitempty"`
	Captured   bool              `json:"captured,omitempty"`
	Code       string            `json:"code,omitempty"`  // error: session_expired | forbidden | service_unavailable | service_error | not_found | llm_busy
	Retry      string            `json:"retry,omitempty"` // error: what the user can do next
	Fields     map[string]string `json:"fields,omitempty"`
	Data       any               `json:"data,omitempty"`
}

// Chip is a follow-up suggestion; clicking sends Message.
type Chip struct {
	Label   string `json:"label"`
	Message string `json:"message"`
}

// Finalize fills the legacy Fields map from the typed content.
func (c *Card) Finalize() {
	if c.Fields != nil {
		return
	}
	f := map[string]string{}
	if c.Message != "" {
		f["Message"] = c.Message
	}
	if c.Retry != "" {
		f["What to do"] = c.Retry
	}
	for _, r := range c.Rows {
		v := r.Value
		if r.Hint != "" {
			v += " (" + r.Hint + ")"
		}
		f[r.Label] = v
	}
	for i, it := range c.Items {
		f[fmt.Sprintf("%02d. %s", i+1, it.Title)] = strings.TrimSpace(it.Subtitle + " " + strings.Join(it.Badges, " · "))
	}
	if c.Total > len(c.Items) && len(c.Items) > 0 {
		f["More"] = fmt.Sprintf("%d more", c.Total-len(c.Items))
	}
	if cp := c.Comparison; cp != nil {
		f[cp.Current.Label] = cp.Current.Display
		f[cp.Previous.Label] = cp.Previous.Display
		if cp.Delta != nil {
			d := fmt.Sprintf("%+.2f", *cp.Delta)
			if cp.DeltaPct != nil {
				d += fmt.Sprintf(" (%+.1f%%)", *cp.DeltaPct)
			}
			f["Change"] = d
		}
	}
	for _, ch := range c.Checklist {
		f[ch.Label] = strings.TrimSpace(strings.ToUpper(ch.Status) + " " + ch.Detail)
	}
	for i, s := range c.Steps {
		f[fmt.Sprintf("Step %d", i+1)] = s
	}
	for i, o := range c.Options {
		f[fmt.Sprintf("Option %d", i+1)] = o.Label
	}
	for i, ct := range c.Citations {
		f[fmt.Sprintf("Source %d", i+1)] = ct.Title + " (" + ct.Source + ")"
	}
	if c.Link != nil {
		f["Open"] = c.Link.Label + " → " + c.Link.Route
	}
	if len(f) > 0 {
		c.Fields = f
	}
}

// Helpers.

func Text(msg string) Card { return Card{Type: TypeText, Title: "Genie", Message: msg} }

func Navigate(intent, title string, link Link) Card {
	return Card{Type: TypeNavigate, Intent: intent, Title: title, Link: &link}
}

// Denied never includes data or hints; it names who can help.
func Denied(intent, feature string) Card {
	msg := "That isn't available for your role."
	if feature != "" {
		msg = fmt.Sprintf("That isn't available for your role. A manager or admin can grant access to %s.", feature)
	}
	return Card{Type: TypeDenied, Intent: intent, Title: "Not available for your role", Message: msg}
}

// Error is a readable failure card (no stack traces, URLs or tokens).
func Error(intent, code, title, message, retry string, link *Link) Card {
	return Card{Type: TypeError, Intent: intent, Code: code, Title: title, Message: message, Retry: retry, Link: link}
}

func NotSupported(intent, what string, link *Link) Card {
	msg := "I can't do that yet - I've logged it for the team."
	if what != "" {
		msg = fmt.Sprintf("I can't %s yet - I've logged it for the team.", what)
	}
	if link != nil {
		msg += " Here's the fastest way to do it yourself."
	}
	return Card{Type: TypeNotSupported, Intent: intent, Title: "Not supported yet", Message: msg, Link: link, Captured: true}
}

func Clarify(intent, question string, opts ...Option) Card {
	return Card{Type: TypeClarify, Intent: intent, Title: question, Options: opts}
}
