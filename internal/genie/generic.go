package genie

import (
	"encoding/json"
	"strings"
	"time"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
	"asi-genie-poc/toolregistry/generated"
)

func (e *Engine) generic(rc *reqCtx, it intents.Intent, s Slots) error {
	switch it.ResponseType {
	case intents.RTNavigate:
		rc.resp.Intent.Handler = "navigate"
		link := moduleLink(it.Module, rc.pid)
		if r, ok := findRoute(s.Destination + " " + rc.req.Message); ok && rc.pid > 0 {
			link = r.link(rc.pid, nil)
		}
		sub := "I can't do that from here yet — use the link below to continue."
		if strings.TrimSpace(it.UserSees) != "" {
			sub = it.UserSees
		}
		rc.add(navigateFromModuleLink(it.ID, sub, link))
		return nil
	case intents.RTGuide:
		rc.resp.Intent.Handler = "navigate"
		return e.guideFallback(rc, it)
	case intents.RTNotSupported:
		rc.resp.Intent.Handler = "fallback"
		l := navigateLinkAction(moduleLink(it.Module, rc.pid))
		rc.add(cards.NotSupported(it.ID, "", &l))
		e.logEvent(rc, "unsupported", it.ID, it.ScenarioID, truncateStr(rc.req.Message, 500))
		return nil
	case intents.RTDenied:
		rc.resp.Intent.Handler = "fallback"
		rc.add(cards.Denied(it.ID, ""))
		return nil
	case intents.RTClarify:
		rc.resp.Intent.Handler = "fallback"
		rc.add(cards.Clarify(it.ID, "Could you give me a bit more detail?"))
		return nil
	case intents.RTDisambiguate:
		rc.resp.Intent.Handler = "dedicated"
		return hLookupReservation(rc, it, s)
	}
	rc.resp.Intent.Handler = "generic"
	return e.genericRead(rc, it, s)
}

func (e *Engine) genericRead(rc *reqCtx, it intents.Intent, s Slots) error {
	link := moduleLink(it.Module, rc.pid)
	bound := toolsForIntent(it.ID, s)
	if len(bound) == 0 {
		rc.add(navigateFromModuleLink(it.ID, "Here's where to find that.", link))
		return nil
	}
	for _, name := range bound {
		t, ok := e.Tools[name]
		if !ok {
			continue
		}
		raw, _ := json.Marshal(map[string]any{"query": map[string]string{"propertyId": rc.pidStr()}})
		if err := e.genericReadFromTool(rc, it, s, link, t, raw); err != nil && !isAuthErr(err) {
			return err
		}
	}
	if len(rc.resp.Cards) == 0 {
		rc.add(navigateFromModuleLink(it.ID, "Here's where to find that.", link))
	}
	return nil
}

func (e *Engine) genericReadFromTool(rc *reqCtx, it intents.Intent, slots Slots, link cards.Link, t generated.ToolMeta, argsJSON json.RawMessage) error {
	argsJSON = forceProperty(t.Path, argsJSON, rc.pidStr())
	args, perr := parseArgs(t, argsJSON)
	if perr != nil {
		return perr
	}
	if args.query.Get("propertyId") == "" && strings.Contains(strings.ToLower(t.Description+t.Path), "propert") {
		args.query.Set("propertyId", rc.pidStr())
	}
	if args.query.Has("propertyId") {
		args.query.Set("propertyId", rc.pidStr())
	}
	start, end, report, ok := explicitReportDates(t.Path, slots, args.query.Get("startDate"), args.query.Get("endDate"))
	if report && !ok {
		rc.clarifyReportDates(it)
		return nil
	}
	if report {
		args.query.Set("startDate", start)
		args.query.Set("endDate", end)
	}
	t0 := time.Now()
	b, gerr := e.execTool(rc.ctx, rc.req.SessionID, rc.token, t, args.path, args.query, argsJSON)
	st := ToolCallStatus{Name: t.Name, Args: argsJSON, Status: "executed", DurationMs: time.Since(t0).Milliseconds()}
	if gerr != nil {
		st.Status, st.Error = "error", errorCode(gerr)
		rc.lastErrService = t.Service
	}
	rc.resp.ToolCalls = append(rc.resp.ToolCalls, st)
	if gerr != nil {
		return gerr
	}
	data := decode(b)
	rc.attachSessionNames(t.Name, data)
	used := t.Description
	c := cards.Card{Intent: it.ID, Title: used, Link: &link}
	list := findList(data)
	if len(list) > 0 && it.ResponseType != intents.RTData {
		c.Type = cards.TypeList
		for i, m := range list {
			if i >= 10 {
				break
			}
			title := personName(m)
			if title == "" {
				title = str(m, "name", "title", "roomNumber", "reservationNumber", "description")
			}
			sub := []string{}
			for _, f := range scalarRows(m, 3) {
				sub = append(sub, f.Label+": "+f.Value)
			}
			c.Items = append(c.Items, cards.ListItem{ID: recordID(m), Title: orDash(title), Subtitle: strings.Join(sub, " · ")})
		}
		total, known, note := pageCount(data, len(list), 10, len(c.Items))
		c.Total = shownTotal(total, known)
		c.Subtitle = note
	} else {
		c.Type = cards.TypeData
		m := obj(data)
		if d := obj(path(m, "data")); d != nil {
			m = d
		}
		c.Rows = scalarRows(m, 12)
		if len(c.Rows) == 0 && len(list) > 0 {
			c.Rows = scalarRows(list[0], 12)
		}
	}
	rc.add(c)
	rc.text("Here's what I found.")
	return nil
}

func (e *Engine) guideFallback(rc *reqCtx, it intents.Intent) error {
	link := navigateLinkAction(moduleLink(it.Module, rc.pid))
	if r, ok := findRoute(rc.req.Message); ok && rc.pid > 0 {
		link = navigateLinkAction(r.link(rc.pid, nil))
	}
	rc.add(cards.NotSupported(it.ID, "answer how-to questions", &link))
	e.logEvent(rc, "unsupported", it.ID, it.ScenarioID, truncateStr(rc.req.Message, 500))
	return nil
}

func (rc *reqCtx) attachSessionNames(tool string, data any) {
	if tool != "auth_get_device_details" && tool != "auth_get_user_login_activity" {
		return
	}
	rows := findList(data)
	if len(rows) == 0 {
		return
	}
	me := rc.currentUser()
	var lookup []map[string]any
	for _, row := range rows {
		id := str(row, "userId")
		if id != "" && id != me.id {
			if v, err := rc.get("config_get_properties_id_users_lookup", map[string]string{"id": rc.pidStr()}, nil); err == nil {
				lookup = findList(v)
			}
			break
		}
	}
	applySessionNames(rows, me.id, me.name, lookup)
}

func applySessionNames(rows []map[string]any, callerID, callerName string, lookup []map[string]any) {
	names := map[string]string{}
	if callerID != "" && callerName != "" {
		names[callerID] = callerName
	}
	for _, u := range lookup {
		id := str(u, "userId", "id")
		if id == "" || names[id] != "" {
			continue
		}
		if n := personName(u); n != "" {
			names[id] = n
		}
	}
	for _, row := range rows {
		if n := names[str(row, "userId")]; n != "" {
			row["name"] = n
		}
	}
}

func (rc *reqCtx) clarifyReportDates(it intents.Intent) {
	biz := rc.bizDate
	if len(biz) != 10 {
		biz = time.Now().Format("2006-01-02")
	}
	monthStart := biz[:8] + "01"
	rc.add(cards.Clarify(it.ID, "Which dates should this report cover? I need a start and an end.",
		cards.Option{Label: "That day (" + biz + ")", Value: biz, Message: "Report for " + biz},
		cards.Option{Label: "Month to date (" + monthStart + " to " + biz + ")", Value: monthStart + ".." + biz, Message: "Report from " + monthStart + " to " + biz},
	))
}

// {id} is the property only on /properties/{id}; other ids stay as the model sent them.
func forceProperty(path string, raw json.RawMessage, pid string) json.RawMessage {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	changed := false
	for _, k := range []string{"propertyId", "property_id"} {
		if strings.Contains(path, "{"+k+"}") {
			m[k] = pid
			changed = true
		}
	}
	if strings.Contains(path, "/properties/{id}") {
		m["id"] = pid
		changed = true
	}
	if q, ok := m["query"].(map[string]any); ok {
		for _, k := range []string{"propertyId", "property_id", "propertyIds"} {
			if _, exists := q[k]; exists {
				q[k] = pid
				changed = true
			}
		}
		m["query"] = q
	}
	if !changed {
		return raw
	}
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}
