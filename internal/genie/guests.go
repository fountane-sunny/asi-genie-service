package genie

import (
	"fmt"
	"regexp"
	"strings"

	"asi-genie-poc/internal/cards"
)

var guestIDInMessage = regexp.MustCompile(`(?i)guest id ([0-9a-f-]{36})`)

func parseGuestIDFromLookupMessage(message string) string {
	m := guestIDInMessage.FindStringSubmatch(message)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func dedupeGuestRecords(list []map[string]any) []map[string]any {
	seen := map[string]map[string]any{}
	order := make([]string, 0, len(list))
	for _, g := range list {
		id := recordID(g)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = g
		order = append(order, id)
	}
	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out
}

func guestOptionLabel(g map[string]any, name string) string {
	name = strings.TrimSpace(name)
	email := strings.TrimSpace(str(g, "email"))
	phone := strings.TrimSpace(str(g, "phone", "phoneNumber", "mobile"))
	city := strings.TrimSpace(str(g, "city"))
	lowerName := strings.ToLower(name)

	if email != "" && !strings.Contains(lowerName, strings.ToLower(email)) {
		return name + " · " + email
	}
	if phone != "" && !strings.Contains(lowerName, strings.ToLower(phone)) {
		return name + " · " + phone
	}
	if city != "" && !strings.Contains(lowerName, strings.ToLower(city)) {
		return name + " · " + city
	}
	return name
}

func guestDisambiguationOptions(list []map[string]any) []cards.Option {
	list = dedupeGuestRecords(list)
	opts := make([]cards.Option, 0, len(list))
	labelCount := map[string]int{}
	for _, g := range list {
		gid := recordID(g)
		if gid == "" {
			continue
		}
		name := strings.TrimSpace(personName(g))
		label := guestOptionLabel(g, name)
		labelCount[label]++
		opts = append(opts, cards.Option{
			Label:   label,
			Value:   gid,
			Message: fmt.Sprintf("Look up guest %s (guest id %s)", name, gid),
		})
	}
	for i, o := range opts {
		if labelCount[o.Label] > 1 {
			short := o.Value
			if len(short) > 8 {
				short = short[len(short)-8:]
			}
			opts[i].Label = o.Label + " · …" + short
		}
	}
	return opts
}
