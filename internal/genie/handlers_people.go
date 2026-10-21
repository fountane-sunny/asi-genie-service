package genie

import (
	"fmt"
	"log/slog"
	"strings"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

// hCurrentUser answers "who am I?" from the signed-in user, then the property
// user row for phone, role, and last login when the auth payload omits them.
func hCurrentUser(rc *reqCtx, it intents.Intent, s Slots) error {
	v, err := rc.get("auth_get_current_user", nil, nil)
	if err != nil {
		slog.Error("current user lookup failed", "err", err, "propertyId", rc.pid)
		return err
	}
	profile := unwrapRecord(v)
	extra := map[string]any{}
	if email := str(profile, "email"); email != "" {
		pv, uerr := rc.get("config_get_properties_id_users", map[string]string{"id": rc.pidStr()}, q("searchKey", email, "pageSize", "5"))
		if uerr != nil {
			slog.Warn("property user enrichment failed", "err", uerr, "propertyId", rc.pid)
		} else if rows := findList(pv); len(rows) > 0 {
			extra = rows[0]
		}
	}
	name := firstNonEmpty(personName(profile), personName(extra))
	rc.add(userProfileCard(it.ID, name, profile, extra, rc.pid))
	rc.text(orDash(name) + " is the signed-in user.")
	return nil
}

// hListRooms lists rooms, or only vacant rooms when the question asks for availability.
func hListRooms(rc *reqCtx, it intents.Intent, s Slots) error {
	v, err := rc.get("config_get_room_space_statuses", nil, q("propertyId", rc.pidStr(), "pageSize", "100"))
	if err != nil {
		slog.Warn("room space status list failed", "err", err, "propertyId", rc.pid)
		v, err = rc.get("config_get_rooms", nil, q("propertyId", rc.pidStr(), "pageSize", "100"))
		if err != nil {
			slog.Error("room list failed", "err", err, "propertyId", rc.pid)
			return err
		}
	}
	rows := findList(v)
	availableOnly := s.Metric == "available"
	var items []cards.ListItem
	for _, m := range rows {
		status := str(m, "roomStatusName", "statusName", "status", "housekeepingStatus")
		if availableOnly && !strings.Contains(strings.ToLower(status), "vacant") {
			continue
		}
		title := roomLabel(m)
		if title == "" {
			title = str(m, "name", "roomName")
		}
		item := cards.ListItem{
			ID:       recordID(m),
			Title:    orDash(title),
			Subtitle: joinParts([]string{str(m, "roomTypeName", "roomType"), str(m, "floorName", "floor"), str(m, "buildingName", "building")}),
		}
		if status != "" {
			item.Badges = []string{status}
		}
		items = append(items, item)
	}
	total := len(items)
	if !availableOnly {
		total = findTotal(v, len(rows))
	}
	shown := items
	if len(shown) > 20 {
		shown = shown[:20]
	}
	title := "Rooms"
	if availableOnly {
		title = "Vacant rooms"
	}
	rc.add(cards.Card{
		Type: cards.TypeList, Intent: it.ID, Title: title, Items: shown, Total: total,
		Link: &cards.Link{Label: "Open room status", Route: p(rc.pid, "/room-space-status-change")},
	})
	if availableOnly {
		rc.text(fmt.Sprintf("%d vacant rooms.", len(items)))
	} else {
		rc.text(fmt.Sprintf("%d rooms.", total))
	}
	return nil
}

// renderPropertyUser searches staff when a guest lookup finds nobody.
// It returns false when there is no matching user.
func (rc *reqCtx) renderPropertyUser(it intents.Intent, name string) (bool, error) {
	v, err := rc.get("config_get_properties_id_users", map[string]string{"id": rc.pidStr()}, q("searchKey", name, "pageSize", "10"))
	if err != nil {
		return false, err
	}
	list := findList(v)
	if len(list) == 0 {
		return false, nil
	}
	if len(list) > 1 {
		opts := make([]cards.Option, 0, len(list))
		for _, u := range list {
			label := firstNonEmpty(personName(u), str(u, "email", "userName", "username"))
			if label == "" {
				continue
			}
			opts = append(opts, cards.Option{Label: label, Value: recordID(u), Message: "Who is " + label})
		}
		rc.add(cards.Card{Type: cards.TypeDisambiguation, Intent: it.ID, Title: fmt.Sprintf("Several people match %q — which one?", name), Options: opts})
		rc.text(fmt.Sprintf("Several people match %q.", name))
		return true, nil
	}
	u := list[0]
	who := firstNonEmpty(personName(u), name)
	rc.add(userProfileCard(it.ID, who, u, nil, rc.pid))
	rc.remember(Entity{Kind: "user", ID: recordID(u), Label: who})
	rc.text(who + " is a user on this property.")
	return true, nil
}

func userProfileCard(intentID, name string, primary, extra map[string]any, pid int) cards.Card {
	pick := func(keys ...string) string {
		if v := str(primary, keys...); v != "" {
			return v
		}
		return str(extra, keys...)
	}
	role := pick("roleName", "role", "title")
	rows := nonEmptyRows([]cards.Field{
		{Label: "Full name", Value: firstNonEmpty(name, pick("fullName", "name"))},
		{Label: "Username", Value: pick("userName", "username")},
		{Label: "Role", Value: role},
		{Label: "Email", Value: pick("email")},
		{Label: "Phone", Value: pick("phone", "phoneNumber", "mobile")},
		{Label: "Last login", Value: pick("lastLoginAt", "lastLogin", "lastLoginTime")},
	})
	return cards.Card{
		Type: cards.TypeData, Intent: intentID, Title: orDash(name), Subtitle: role, Rows: rows,
		Image: firstNonEmpty(recordImage(primary), recordImage(extra)),
		Link:  &cards.Link{Label: "Open users", Route: fmt.Sprintf("/users/%d", pid)},
	}
}

func unwrapRecord(v any) map[string]any {
	m := obj(v)
	if m == nil {
		return map[string]any{}
	}
	if d := obj(path(m, "data")); d != nil {
		return d
	}
	return m
}

func joinParts(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return strings.Join(out, " · ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
