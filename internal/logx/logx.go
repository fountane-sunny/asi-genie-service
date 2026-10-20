// Package logx prints readable, boxed log blocks for every Genie call:
// inbound /chat, LLM calls and ASI calls. Secrets and card numbers are masked.
//
// Env:
//
//	LOG_LEVEL   debug|info   debug shows bodies (default debug when APP_ENV=local, else info)
//	LOG_BODIES  true|false   force bodies on/off regardless of level
//	LOG_BODY_MAX             max chars per body (default 4000)
//	LOG_COLOR   false        disable ANSI colours (also off when stderr isn't a TTY)
package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	once     sync.Once
	bodies   bool
	bodyMax  = 4000
	color    bool
	writeMu  sync.Mutex
	panRe    = regexp.MustCompile(`(?:\d[ -]?){12,18}\d`)
	secretRe = regexp.MustCompile(`(?i)("?(?:x-?token|authorization|cookie|set-cookie|password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|token|card[_-]?number|cvv|cvc)"?\s*[:=]\s*)("[^"]*"|[^\s,&}]+)`)
	bearerRe = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`)
	keyRe    = regexp.MustCompile(`AIza[0-9A-Za-z_\-]{20,}|sk-[A-Za-z0-9_\-]{20,}`)
)

func setup() {
	once.Do(func() {
		lvl := strings.ToLower(os.Getenv("LOG_LEVEL"))
		if lvl == "" {
			if strings.EqualFold(os.Getenv("APP_ENV"), "local") {
				lvl = "debug"
			} else {
				lvl = "info"
			}
		}
		bodies = lvl == "debug"
		if v := os.Getenv("LOG_BODIES"); v != "" {
			bodies, _ = strconv.ParseBool(v)
		}
		if n, err := strconv.Atoi(os.Getenv("LOG_BODY_MAX")); err == nil && n > 0 {
			bodyMax = n
		}
		color = isTTY() && !strings.EqualFold(os.Getenv("LOG_COLOR"), "false")
	})
}

func isTTY() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ---- masking ----

func luhn(d string) bool {
	sum, alt := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if alt {
			if n *= 2; n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}

// MaskPAN replaces Luhn-valid card numbers with "•••• •••• •••• 1234".
func MaskPAN(s string) (string, bool) {
	found := false
	out := panRe.ReplaceAllStringFunc(s, func(m string) string {
		d := strings.NewReplacer(" ", "", "-", "").Replace(m)
		if len(d) >= 13 && len(d) <= 19 && luhn(d) {
			found = true
			return "•••• •••• •••• " + d[len(d)-4:]
		}
		return m
	})
	return out, found
}

// Mask hides tokens, API keys, cookies, passwords and card numbers.
func Mask(s string) string {
	s = bearerRe.ReplaceAllString(s, "Bearer ***")
	s = keyRe.ReplaceAllString(s, "***")
	s = secretRe.ReplaceAllStringFunc(s, func(m string) string {
		sm := secretRe.FindStringSubmatch(m)
		if strings.HasPrefix(sm[2], `"`) {
			return sm[1] + `"***"`
		}
		return sm[1] + "***"
	})
	s, _ = MaskPAN(s)
	return s
}

// ---- formatting ----

// Pretty returns masked, indented JSON (or masked text), truncated to LOG_BODY_MAX.
func Pretty(v any) string {
	setup()
	var raw []byte
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		raw = x
	case json.RawMessage:
		raw = x
	case string:
		raw = []byte(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			raw = []byte(fmt.Sprint(x))
		} else {
			raw = b
		}
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	s := string(raw)
	if (raw[0] == '{' || raw[0] == '[') && json.Indent(&buf, raw, "", "  ") == nil {
		s = buf.String()
	}
	s = Mask(s)
	if r := []rune(s); len(r) > bodyMax {
		s = string(r[:bodyMax]) + fmt.Sprintf("\n… [truncated %d of %d chars; LOG_BODY_MAX=%d]", len(r)-bodyMax, len(r), bodyMax)
	}
	return s
}

// Trunc shortens s to n runes (for system prompts etc.).
func Trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf(" … [+%d chars]", len(r)-n)
}

// Field is one labelled line or section of a block.
type Field struct {
	Label string
	Value string // single line
	Body  string // multi-line (only printed when bodies are on)
	Force bool   // print Body even at info level
}

// F builds a one-line field.
func F(label string, v any) Field { return Field{Label: label, Value: Mask(fmt.Sprint(v))} }

// B builds a body section (shown at debug level / LOG_BODIES=true).
func B(label string, v any) Field { return Field{Label: label, Body: Pretty(v)} }

const (
	cReset = "\033[0m"
	cDim   = "\033[2m"
	cBold  = "\033[1m"
	cRed   = "\033[31m"
	cGreen = "\033[32m"
	cYel   = "\033[33m"
	cBlue  = "\033[34m"
	cMag   = "\033[35m"
	cCyan  = "\033[36m"
)

func paint(c, s string) string {
	if !color {
		return s
	}
	return c + s + cReset
}

var kindColor = map[string]string{"CHAT": cCyan, "LLM": cMag, "ASI": cBlue}

// Block prints one boxed entry: "── KIND ▶ title ─── status · duration".
// ok=false paints the status red.
func Block(kind, title, status string, d time.Duration, ok bool, fields ...Field) {
	setup()
	var b strings.Builder
	head := paint(cBold+kindColor[kind], "── "+kind+" ▶ "+title)
	st := status
	if st != "" {
		if ok {
			st = paint(cGreen, st)
		} else {
			st = paint(cRed, st)
		}
	}
	fmt.Fprintf(&b, "%s %s %s · %s\n", head, paint(cDim, "──"), st, d.Round(time.Millisecond))
	for _, f := range fields {
		if f.Body == "" && f.Value == "" {
			continue
		}
		if f.Body != "" {
			if !bodies && !f.Force {
				continue
			}
			fmt.Fprintf(&b, "  %s\n", paint(cYel, f.Label+":"))
			for _, line := range strings.Split(f.Body, "\n") {
				fmt.Fprintf(&b, "  %s %s\n", paint(cDim, "│"), line)
			}
			continue
		}
		fmt.Fprintf(&b, "  %-10s %s\n", paint(cYel, f.Label), f.Value)
	}
	b.WriteString(paint(cDim, "  └"+strings.Repeat("─", 60)) + "\n")
	writeMu.Lock()
	fmt.Fprintf(os.Stderr, "\n%s %s", time.Now().Format("15:04:05.000"), b.String())
	writeMu.Unlock()
}

// Line prints a single short log line (e.g. non-chat HTTP requests).
func Line(format string, args ...any) {
	writeMu.Lock()
	fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("15:04:05.000"), Mask(fmt.Sprintf(format, args...)))
	writeMu.Unlock()
}

// ---- context: which tool an ASI call belongs to ----

type toolKey struct{}

// WithTool tags ctx so the HTTP client can name the tool in its log block.
func WithTool(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, toolKey{}, name)
}

// Tool returns the tool name set by WithTool.
func Tool(ctx context.Context) string {
	s, _ := ctx.Value(toolKey{}).(string)
	return s
}
