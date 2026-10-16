package genie

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
	genillm "asi-genie-poc/llm"
	"asi-genie-poc/toolregistry/generated"
)

// generic handles P1/P2 capabilities by catalogue response type.
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

// genericRead lets the LLM pick ONE read endpoint (GET-only, top 12 by the
// selector) and its parameters; the result is rendered server-side as the
// intent's card type. The model only phrases a one-line lead-in.
func (e *Engine) genericRead(rc *reqCtx, it intents.Intent, s Slots) error {
	link := moduleLink(it.Module, rc.pid)
	if e.LLM == nil {
		rc.add(navigateFromModuleLink(it.ID, "Here's where to find that.", link))
		return nil
	}
	if rc.llmFailed {
		// Classifier already failed (quota/timeout): skip a second LLM call; try the top
		// keyword-selected GET tool with propertyId only.
		sel := e.Selector.Select(rc.req.Message+" "+it.ID+" "+it.Handling, 1)
		if len(sel) > 0 {
			raw, _ := json.Marshal(map[string]any{"query": map[string]string{"propertyId": rc.pidStr()}})
			if err := e.genericReadFromTool(rc, it, link, sel[0], raw); err == nil {
				return nil
			} else if !isAuthErr(err) {
				return err
			}
		}
		rc.add(navigateFromModuleLink(it.ID, "The AI is slow right now, so I couldn't fetch that. Try again in a moment or use the link below.", link))
		return nil
	}
	sel := e.Selector.Select(rc.req.Message+" "+it.ID+" "+it.Handling, 12)
	if len(sel) == 0 {
		rc.add(navigateFromModuleLink(it.ID, "Here's where to find that.", link))
		return nil
	}
	tools := make([]genillm.Tool, 0, len(sel))
	allowed := map[string]bool{}
	for _, t := range sel {
		tools = append(tools, toLLMTool(t))
		allowed[t.Name] = true
	}
	slotsJSON, _ := json.Marshal(s)
	system := fmt.Sprintf(`You fetch data for ASI Genie capability %q (%s).
How it is handled: %s
Call exactly ONE of the provided read endpoints with the right parameters. propertyId=%d. Business date=%s. Slots=%s.
Use path parameter "id" = propertyId when the path is /properties/{id}/... .`, it.ID, it.Module, it.Handling, rc.pid, rc.bizDate, slotsJSON)
	msgs := []genillm.Message{{Role: genillm.RoleUser, Content: rc.req.Message}}
	ctx, cancel := rc.llmCtx()
	res, err := e.LLM.SendMessage(ctx, system, msgs, tools)
	cancel()
	if err != nil {
		return err
	}
	for _, tc := range res.ToolCalls {
		t, ok := e.Tools[tc.Name]
		if !ok || !allowed[tc.Name] {
			continue
		}
		if err := e.genericReadFromTool(rc, it, link, t, tc.Arguments); err == nil {
			return nil
		} else if !isAuthErr(err) {
			return err
		}
	}
	rc.add(navigateFromModuleLink(it.ID, "Here's where to find that.", link))
	return nil
}

// genericReadFromTool executes one read tool and renders a list/data card for the intent.
func (e *Engine) genericReadFromTool(rc *reqCtx, it intents.Intent, link cards.Link, t generated.ToolMeta, argsJSON json.RawMessage) error {
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
		c.Total = findTotal(data, len(list))
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

// guideFallback: Phase 1 has no help-doc corpus, so how-to questions get an
// honest "not supported yet" plus the screen where the task is done.
func (e *Engine) guideFallback(rc *reqCtx, it intents.Intent) error {
	link := navigateLinkAction(moduleLink(it.Module, rc.pid))
	if r, ok := findRoute(rc.req.Message); ok && rc.pid > 0 {
		link = navigateLinkAction(r.link(rc.pid, nil))
	}
	rc.add(cards.NotSupported(it.ID, "answer how-to questions", &link))
	e.logEvent(rc, "unsupported", it.ID, it.ScenarioID, truncateStr(rc.req.Message, 500))
	return nil
}

// forceProperty pins property path and query params to the property the user
// is on. {id} is the property only on /properties/{id}/...; a reservation or
// guest {id} is left as the model sent it.
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
