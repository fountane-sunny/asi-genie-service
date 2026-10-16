package genie

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"asi-genie-poc/internal/cards"
)

// Helpers for reading loosely-typed ASI JSON responses.

func decode(b []byte) any {
	var v any
	if len(b) == 0 || json.Unmarshal(b, &v) != nil {
		return nil
	}
	return v
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// lookup returns m[key] matching case-insensitively.
func lookup(m map[string]any, key string) (any, bool) {
	if m == nil {
		return nil, false
	}
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// path walks dotted keys: path(v, "data", "items").
func path(v any, keys ...string) any {
	for _, k := range keys {
		x, ok := lookup(obj(v), k)
		if !ok {
			return nil
		}
		v = x
	}
	return v
}

func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "Yes"
		}
		return "No"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', 2, 64)
	case json.Number:
		return x.String()
	}
	return ""
}

// str returns the first non-empty scalar among keys.
func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := lookup(m, k); ok {
			if s := scalarString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func num(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		v, ok := lookup(m, k)
		if !ok {
			continue
		}
		switch x := v.(type) {
		case float64:
			return x, true
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

var listKeys = []string{"data", "items", "results", "rows", "reservations", "records", "list", "content", "guests", "values"}

// findList extracts the main array of objects from a response.
func findList(v any) []map[string]any {
	return findListDepth(v, 0)
}

func findListDepth(v any, depth int) []map[string]any {
	if arr, ok := v.([]any); ok {
		out := make([]map[string]any, 0, len(arr))
		for _, x := range arr {
			if m := obj(x); m != nil {
				out = append(out, m)
			}
		}
		return out
	}
	m := obj(v)
	if m == nil || depth > 2 {
		return nil
	}
	for _, k := range listKeys {
		if x, ok := lookup(m, k); ok {
			if l := findListDepth(x, depth+1); l != nil {
				return l
			}
		}
	}
	return nil
}

// explicitTotal is a pagination total the API actually sent.
func explicitTotal(v any) (int, bool) {
	m := obj(v)
	for _, cand := range []map[string]any{m, obj(path(v, "data")), obj(path(v, "pagination")), obj(path(v, "meta")), obj(path(v, "pageInfo"))} {
		if n, ok := num(cand, "total", "totalCount", "totalRecords", "totalItems", "count"); ok {
			return int(n), true
		}
	}
	return 0, false
}

// pageCount distinguishes a real total from a full page. known is false when
// the API sent no total and the page is full, so listLen must not be shown as
// the complete count. note is set when the card shows only the first rows.
func pageCount(v any, listLen, pageSize, shown int) (total int, known bool, note string) {
	if shown > listLen {
		shown = listLen
	}
	if n, ok := explicitTotal(v); ok {
		if n < listLen {
			n = listLen
		}
		if shown < n {
			return n, true, fmt.Sprintf("showing the first %d", shown)
		}
		return n, true, ""
	}
	if pageSize > 0 && listLen >= pageSize {
		return listLen, false, fmt.Sprintf("showing the first %d", shown)
	}
	return listLen, true, ""
}

// findTotal returns a total count from common pagination shapes, or fallback.
func findTotal(v any, fallback int) int {
	m := obj(v)
	for _, cand := range []map[string]any{m, obj(path(v, "data")), obj(path(v, "pagination")), obj(path(v, "meta")), obj(path(v, "pageInfo"))} {
		if n, ok := num(cand, "total", "totalCount", "totalRecords", "totalItems", "count"); ok && n >= float64(fallback) {
			return int(n)
		}
	}
	return fallback
}

// deepFind returns the first value whose key matches pred (depth-first, bounded).
func deepFind(v any, pred func(string) bool, depth int) (any, bool) {
	if depth > 4 {
		return nil, false
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if pred(k) {
				return x[k], true
			}
		}
		for _, k := range keys {
			if r, ok := deepFind(x[k], pred, depth+1); ok {
				return r, true
			}
		}
	case []any:
		for _, e := range x {
			if r, ok := deepFind(e, pred, depth+1); ok {
				return r, true
			}
		}
	}
	return nil, false
}

func deepStr(v any, keys ...string) string {
	for _, k := range keys {
		kk := k
		if r, ok := deepFind(v, func(s string) bool { return strings.EqualFold(s, kk) }, 0); ok {
			if s := scalarString(r); s != "" {
				return s
			}
		}
	}
	return ""
}

// metricValue reads a number from a scalar or a metric object.
func metricValue(v any, keys ...string) string {
	if s := scalarString(v); s != "" {
		return s
	}
	if len(keys) == 0 {
		keys = []string{"displayValue", "value", "count", "total", "expected", "reservationCount", "roomsEstimate"}
	}
	return str(obj(v), keys...)
}

var camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func humanize(k string) string {
	k = camelRe.ReplaceAllString(k, "$1 $2")
	k = strings.ReplaceAll(k, "_", " ")
	r := []rune(k)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

var skipKeyRe = regexp.MustCompile(`(?i)(password|token|secret|card ?number|cvv|pan$|^id$|uuid|createdBy|updatedBy|deletedAt)`)

// scalarRows turns top-level scalar fields into ordered rows (stable, limited).
func scalarRows(m map[string]any, limit int) []cards.Field {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if skipKeyRe.MatchString(k) {
			continue
		}
		if scalarString(v) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []cards.Field
	for _, k := range keys {
		out = append(out, cards.Field{Label: humanize(k), Value: scalarString(m[k])})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// personName extracts a guest/person display name from a record.
func personName(m map[string]any) string {
	if s := str(m, "guestName", "primaryGuestName", "fullName", "pocName", "name", "displayName"); s != "" {
		return s
	}
	if fn, ln := str(m, "firstName", "first_name"), str(m, "lastName", "last_name"); fn+ln != "" {
		return strings.TrimSpace(fn + " " + ln)
	}
	for _, k := range []string{"guest", "primaryGuest", "poc", "contact"} {
		if g := obj(path(m, k)); g != nil {
			if s := personName(g); s != "" {
				return s
			}
		}
	}
	return ""
}

func reservationNumber(m map[string]any) string {
	return str(m, "reservationNumber", "confirmationNumber", "confirmationNo", "reservationNo", "bookingNumber", "rezNumber")
}

func recordID(m map[string]any) string {
	return str(m, "id", "reservationId", "reservation_id", "guestId", "uuid")
}

func roomLabel(m map[string]any) string {
	if s := str(m, "roomNumber", "roomName", "roomNo", "room"); s != "" {
		return s
	}
	if l := findList(path(m, "rooms")); len(l) > 0 {
		return str(l[0], "roomNumber", "roomName", "name")
	}
	if s := deepStr(m, "roomNumber", "roomName"); s != "" {
		return s
	}
	return ""
}

func money(f float64, currency string) string {
	if currency == "" {
		currency = "$"
	} else if currency == "USD" {
		currency = "$"
	} else {
		currency += " "
	}
	return fmt.Sprintf("%s%.2f", currency, f)
}
