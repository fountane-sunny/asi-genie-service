package genie

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	genillm "asi-genie-poc/llm"
	"asi-genie-poc/toolregistry/generated"
)

var (
	wordRe      = regexp.MustCompile(`[a-z0-9]+`)
	pathParamRe = regexp.MustCompile(`\{([^}]+)\}`)
	stopWords   = map[string]bool{"the": true, "a": true, "an": true, "is": true, "of": true, "for": true, "to": true, "me": true, "my": true, "and": true, "in": true, "on": true, "what": true, "show": true, "get": true, "list": true, "all": true, "please": true, "how": true, "many": true, "are": true, "there": true, "i": true, "can": true, "you": true, "with": true, "api": true, "v1": true, "do": true, "we": true, "have": true, "today": true, "this": true, "that": true, "give": true, "tell": true, "find": true}
	domainHints = map[string][]string{
		"reservation": {"reservation", "booking", "guest", "arrival", "departure", "checkin", "checkout", "occupancy", "folio", "stay", "ledger", "deposit", "housekeeping", "shift", "collection", "audit", "noshow"},
		"config":      {"room", "rate", "property", "building", "tax", "config", "setting", "amenity", "floor", "plan", "season"},
		"auth":        {"user", "role", "permission", "login", "invite", "member", "staff"},
		"reporting":   {"report", "email", "send", "schedule"},
	}
	synonyms = map[string][]string{
		"booking":   {"reservation"},
		"bookings":  {"reservation"},
		"arriving":  {"arrival"},
		"leaving":   {"departure"},
		"departing": {"departure"},
		"staff":     {"user"},
		"employee":  {"user"},
		"price":     {"rate"},
		"prices":    {"rate"},
		"pricing":   {"rate"},
		"hotel":     {"property"},
		"revenue":   {"revenue", "collection"},
		"payments":  {"payment"},
		"cleaning":  {"housekeeping"},
	}
)

func singular(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return strings.TrimSuffix(w, "ies") + "y"
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return strings.TrimSuffix(w, "s")
	}
	return w
}

func queryTerms(s string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(w string) {
		w = singular(w)
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if stopWords[w] || len(w) < 2 {
			continue
		}
		add(w)
		for _, syn := range synonyms[w] {
			add(syn)
		}
	}
	return out
}

type indexedTool struct {
	meta  generated.ToolMeta
	words map[string]bool
	hay   string
	isExp bool
}

type ToolSelector struct {
	tools []indexedTool
	idf   map[string]float64
	mu    sync.Mutex
	cache map[string][]generated.ToolMeta
}

func NewToolSelector(all []generated.ToolMeta) *ToolSelector {
	s := &ToolSelector{idf: map[string]float64{}, cache: map[string][]generated.ToolMeta{}}
	df := map[string]int{}
	rep := strings.NewReplacer("_", " ", "/", " ", "-", " ")
	for _, t := range all {
		hay := rep.Replace(strings.ToLower(t.Service + " " + t.Name + " " + t.Path + " " + t.Description))
		ws := map[string]bool{}
		for _, w := range wordRe.FindAllString(hay, -1) {
			ws[singular(w)] = true
		}
		for w := range ws {
			df[w]++
		}
		s.tools = append(s.tools, indexedTool{meta: t, words: ws, hay: hay, isExp: ws["export"] || ws["download"]})
	}
	n := float64(len(s.tools))
	for w, d := range df {
		s.idf[w] = math.Log(1 + n/float64(d))
	}
	return s
}

func (s *ToolSelector) score(t indexedTool, terms []string, wantsExport bool) float64 {
	score := 0.0
	matched := 0
	for _, w := range terms {
		if t.words[w] {
			score += 2 * s.idf[w]
			matched++
		} else if len(w) >= 4 && strings.Contains(t.hay, w) {
			score += 0.5 * s.idf[w]
		}
		for _, h := range domainHints[t.meta.Service] {
			if w == h {
				score += 1
				break
			}
		}
	}
	if score == 0 {
		return 0
	}
	if matched > 1 {
		score *= 1 + 0.15*float64(matched-1)
	}
	if t.isExp && !wantsExport {
		score *= 0.5
	}
	return score
}

func (s *ToolSelector) Select(query string, n int) []generated.ToolMeta {
	terms := queryTerms(query)
	key := strings.Join(terms, " ")
	s.mu.Lock()
	if v, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()

	wantsExport := false
	for _, w := range terms {
		if w == "export" || w == "download" || w == "csv" || w == "excel" {
			wantsExport = true
		}
	}
	type scored struct {
		t generated.ToolMeta
		s float64
	}
	var list []scored
	for _, t := range s.tools {
		if v := s.score(t, terms, wantsExport); v > 0 {
			list = append(list, scored{t.meta, v})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].s != list[j].s {
			return list[i].s > list[j].s
		}
		return list[i].t.Name < list[j].t.Name
	})
	if len(list) > n {
		list = list[:n]
	}
	out := make([]generated.ToolMeta, 0, len(list))
	for _, x := range list {
		out = append(out, x.t)
	}
	s.mu.Lock()
	if len(s.cache) > 512 {
		s.cache = map[string][]generated.ToolMeta{}
	}
	s.cache[key] = out
	s.mu.Unlock()
	return out
}

func toLLMTool(t generated.ToolMeta) genillm.Tool {
	props := map[string]any{}
	required := []string{}
	for _, m := range pathParamRe.FindAllStringSubmatch(t.Path, -1) {
		props[m[1]] = map[string]any{"type": "string", "description": "path parameter " + m[1]}
		required = append(required, m[1])
	}
	props["query"] = map[string]any{
		"type":                 "object",
		"description":          "query string parameters (e.g. propertyId, startDate, endDate)",
		"additionalProperties": map[string]any{"type": "string"},
	}
	params := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		params["required"] = required
	}
	descText := t.Description
	if t.Name == "auth_get_permissions" {
		// The generated summary was copied from device-details ("Get user sessions").
		descText = "Returns the caller's permission rows for each property."
	}
	desc := fmt.Sprintf("[%s] %s %s — %s", t.Service, t.Method, t.Path, descText)
	return genillm.Tool{Name: t.Name, Description: desc, Parameters: params}
}

var services = map[string]generated.Service{
	"auth":        generated.AuthService,
	"config":      generated.ConfigService,
	"reporting":   generated.ReportingService,
	"reservation": generated.ReservationService,
}

type toolArgs struct {
	path  string
	query url.Values
}

func parseArgs(t generated.ToolMeta, raw json.RawMessage) (toolArgs, error) {
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return toolArgs{}, fmt.Errorf("invalid tool arguments: %w", err)
		}
	}
	out := toolArgs{path: t.Path, query: url.Values{}}
	for _, p := range pathParamRe.FindAllStringSubmatch(t.Path, -1) {
		v := fmt.Sprint(m[p[1]])
		if m[p[1]] == nil || v == "" {
			return out, fmt.Errorf("missing path parameter %q", p[1])
		}
		out.path = strings.ReplaceAll(out.path, p[0], url.PathEscape(v))
	}
	if q, ok := m["query"].(map[string]any); ok {
		for k, v := range q {
			if v != nil {
				out.query.Set(k, fmt.Sprint(v))
			}
		}
	}
	return out, nil
}

func asJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return b
	}
	j, _ := json.Marshal(string(b))
	return j
}

func IsReadTool(t generated.ToolMeta) bool { return t.Method == http.MethodGet && t.ReadOnly }

// Name or path fragments that must never be offered to the model.
var blockedReadFragments = []string{
	"admin",
	"impersonat",
	"invite",
	"/internal/",
	"audit-log",
	"export",
}

func IsAllowedReadTool(t generated.ToolMeta) bool {
	if !IsReadTool(t) {
		return false
	}
	blob := strings.ToLower(t.Name + " " + t.Path)
	for _, frag := range blockedReadFragments {
		if strings.Contains(blob, frag) {
			return false
		}
	}
	return true
}

const MaxResultBytes = 8000

// Writes Genie may call, and only from a handler after the user confirms. Never offered to the model.
var allowedWriteTools = map[string]bool{
	"reservation_post_folio_id_email": true, // email_folio (G-243), confirmed send only
}

func IsAllowedWriteTool(t generated.ToolMeta) bool {
	return t.Method == http.MethodPost && !t.ReadOnly && allowedWriteTools[t.Name]
}
