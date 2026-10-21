package cards

import (
	"fmt"
	"strings"
)

const (
	TypeText           = "text"
	TypeData           = "data"
	TypeList           = "list"
	TypeComparison     = "comparison"
	TypeChecklist      = "checklist"
	TypeNavigate       = "navigate"
	TypeGuide          = "guide"
	TypeClarify        = "clarify"
	TypeDisambiguation = "disambiguation"
	TypeError          = "error" // never raw errors, stack traces, URLs, or tokens
	TypeDenied         = "denied"
	TypeNotSupported   = "not_supported"
	TypeTable          = "table"
	TypeChart          = "chart"
)

const (
	ChartBar  = "bar"
	ChartLine = "line"
	ChartArea = "area"
	ChartPie  = "pie"
)

type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Align string `json:"align,omitempty"`
}

type TableData struct {
	Columns []Column            `json:"columns"`
	Rows    []map[string]string `json:"rows"`
}

type ChartSeries struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type ChartData struct {
	Kind   string           `json:"kind"`
	XKey   string           `json:"xKey"`
	Series []ChartSeries    `json:"series"`
	Data   []map[string]any `json:"data"`
	Unit   string           `json:"unit,omitempty"`
}

type Link struct {
	Label  string            `json:"label"`
	Route  string            `json:"route"`
	Params map[string]string `json:"params,omitempty"`
}

// Path is relative to the service's public base URL, not an absolute URL.
type Download struct {
	Label    string            `json:"label"`
	FileName string            `json:"fileName"`
	Service  string            `json:"service"`
	Path     string            `json:"path"`
	Query    map[string]string `json:"query,omitempty"`
}

type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

type ListItem struct {
	ID       string   `json:"id,omitempty"`
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Badges   []string `json:"badges,omitempty"`
	Image    string   `json:"image,omitempty"`
	Link     *Link    `json:"link,omitempty"`
}

type Period struct {
	Label   string   `json:"label"`
	Value   *float64 `json:"value,omitempty"`
	Display string   `json:"display"`
}

type Comparison struct {
	Metric   string   `json:"metric"`
	Current  Period   `json:"current"`
	Previous Period   `json:"previous"`
	Delta    *float64 `json:"delta,omitempty"`
	DeltaPct *float64 `json:"deltaPct,omitempty"`
}

const (
	CheckOK      = "ok"
	CheckWarn    = "warn"
	CheckFail    = "fail"
	CheckUnknown = "unknown" // could not be read (missing permission or API error)
)

type CheckItem struct {
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Link   *Link  `json:"link,omitempty"`
}

// Sending Message back as the next chat message resolves the question.
type Option struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Message string `json:"message"`
}

type Citation struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Snippet string `json:"snippet,omitempty"`
}

// Fields is kept so the first chat panel can still render key/value pairs.
type Card struct {
	Type       string            `json:"type"`
	Intent     string            `json:"intent,omitempty"`
	Title      string            `json:"title"`
	Subtitle   string            `json:"subtitle,omitempty"`
	Message    string            `json:"message,omitempty"`
	Image      string            `json:"image,omitempty"` // empty: the panel shows name initials
	Rows       []Field           `json:"rows,omitempty"`
	Items      []ListItem        `json:"items,omitempty"`
	Total      int               `json:"total,omitempty"`
	Comparison *Comparison       `json:"comparison,omitempty"`
	Checklist  []CheckItem       `json:"checklist,omitempty"`
	Options    []Option          `json:"options,omitempty"`
	Steps      []string          `json:"steps,omitempty"`
	Citations  []Citation        `json:"citations,omitempty"`
	Link       *Link             `json:"link,omitempty"`
	Downloads  []Download        `json:"downloads,omitempty"`
	Captured   bool              `json:"captured,omitempty"`
	Code       string            `json:"code,omitempty"`
	Retry      string            `json:"retry,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	Data       any               `json:"data,omitempty"`
	Table      *TableData        `json:"table,omitempty"`
	Chart      *ChartData        `json:"chart,omitempty"`
}

type Chip struct {
	Label   string `json:"label"`
	Message string `json:"message"`
}

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

func Text(msg string) Card { return Card{Type: TypeText, Title: "Genie", Message: msg} }

func Navigate(intent, title string, link Link) Card {
	return Card{Type: TypeNavigate, Intent: intent, Title: title, Link: &link}
}

// Never includes data or hints.
func Denied(intent, feature string) Card {
	msg := "That isn't available for your role."
	if feature != "" {
		msg = fmt.Sprintf("That isn't available for your role. A manager or admin can grant access to %s.", feature)
	}
	return Card{Type: TypeDenied, Intent: intent, Title: "Not available for your role", Message: msg}
}

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
