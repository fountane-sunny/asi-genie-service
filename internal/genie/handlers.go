package genie

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

type handlerFunc func(rc *reqCtx, it intents.Intent, s Slots) error

// dedicatedHandlers maps every P0 capability (plus compare_metrics) to a
// deterministic handler: fixed GET reads, server-side composition, typed card.
func (e *Engine) dedicatedHandlers() map[string]handlerFunc {
	return map[string]handlerFunc{
		"property_snapshot":                hPropertySnapshot,
		"compare_metrics":                  hCompareMetrics,
		"todays_arrivals":                  hArrivals,
		"todays_departures":                hDepartures,
		"check_availability":               hAvailability,
		"check_availability_missing_dates": hMissingDates,
		"lookup_reservation":               hLookupReservation,
		"reservation_policy_check":         hPolicyCheck,
		"folio_balance_check":              hFolioBalance,
		"quick_checkin":                    hCheckinReadiness,
		"quick_checkout":                   hCheckoutReadiness,
		"late_checkout_quote":              hLateCheckout,
		"guide_query":                      hGuide,
		"lookup_guest":                     hLookupGuest,
		"room_status_summary":              hRoomStatusSummary,
		"hk_assignments_read":              hHKAssignments,
		"update_room_status":               hUpdateRoomStatusNav,
		"rate_query":                       hRateQuery,
		"night_audit_status":               hNightAuditStatus,
		"night_audit_readiness":            hNightAuditReadiness,
		"night_audit_walkthrough":          hNightAuditWalkthrough,
		"view_report":                      hViewReport,
		"revenue_query":                    hRevenue,
		"policy_lookup":                    hPolicyLookup,
		"capabilities":                     hCapabilities,
		"navigate":                         hNavigate,
		"navigate_entity":                  hNavigateEntity,
		"denied_intent":                    hDeniedIntent,
		"disambiguate":                     hLookupReservation,
		"pci_block":                        hPCI,
		"context_carry":                    hContextCarry,
		"since_last_shift":                 hSinceLastShift,
		"property_scope_missing":           hPropertyScope,
		"create_reservation":               hCreateReservation,
		"create_reservation_slots":         hCreateReservationSlots,
		"log_service_request":              hLogServiceRequest,
		"clarify_slot":                     hClarifySlot,
		"late_checkout_fee_tier":           hLateCheckoutFeeTier,
	}
}

func (e *Engine) dispatch(rc *reqCtx, it intents.Intent, s Slots, info IntentInfo) error {
	switch it.ID {
	case intents.SmallTalkID:
		rc.resp.Intent.Handler = "fallback"
		rc.text("Hi! I can answer questions about your property, explain how things work, and open the right screen.")
		rc.chips("Who arrives today?", "How's the hotel doing today?", "What can you do?")
		return nil
	case intents.UnsupportedID:
		rc.resp.Intent.Handler = "fallback"
		what := s.Unsupported
		var link *cards.Link
		if r, ok := findRoute(rc.req.Message); ok && rc.pid > 0 {
			l := navigateLinkAction(r.link(rc.pid, nil))
			link = &l
		}
		rc.add(cards.NotSupported(intents.UnsupportedID, what, link))
		e.logEvent(rc, "unsupported", "", "", truncateStr(rc.req.Message, 500))
		rc.chips("What can you do?")
		return nil
	}
	if h, ok := e.handlers[it.ID]; ok {
		rc.resp.Intent.Handler = "dedicated"
		return h(rc, it, s)
	}
	return e.generic(rc, it, s)
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	return v
}

// ---- Dashboard ----

func hPropertySnapshot(rc *reqCtx, it intents.Intent, s Slots) error {
	d := rc.dayOr(s)
	sum, err := rc.get("reservation_get_dashboard_today_summary", nil, q("propertyId", rc.pidStr(), "date", d))
	if err != nil {
		return err
	}
	c := cards.Card{Type: cards.TypeData, Intent: it.ID, Title: "Today at a glance", Subtitle: d}
	arr, dep := obj(path(sum, "arrivals")), obj(path(sum, "departures"))
	c.Rows = append(c.Rows,
		cards.Field{Label: "Arrivals", Value: metricValue(arr, "expected"), Hint: hintDone(arr)},
		cards.Field{Label: "Departures", Value: metricValue(dep, "expected"), Hint: hintDone(dep)},
		cards.Field{Label: "In-house", Value: metricValue(path(sum, "inHouse"), "reservationCount", "roomsEstimate")},
		cards.Field{Label: "Cancelled", Value: metricValue(path(sum, "cancelled"), "reservationCount")},
		cards.Field{Label: "No-shows", Value: metricValue(path(sum, "noShow"), "reservationCount")},
	)
	// KPI tiles: shown when the KPI API answers; a 401/403 from ASI just omits them.
	{
		if kpi, err := rc.get("reservation_get_dashboard_kpi_overview", nil, q("propertyId", rc.pidStr(), "date", d)); err == nil {
			for _, k := range []struct{ key, label string }{{"occupancy", "Occupancy"}, {"adr", "ADR"}, {"revpar", "RevPAR"}} {
				m := obj(path(kpi, k.key))
				if m == nil {
					continue
				}
				f := cards.Field{Label: k.label, Value: str(m, "displayValue", "value")}
				if cp, ok := num(m, "changePercent"); ok {
					f.Hint = fmt.Sprintf("%+.1f%% vs previous day", cp)
				}
				c.Rows = append(c.Rows, f)
			}
		}
	}
	c.Rows = nonEmptyRows(c.Rows)
	l := moduleLink("Dashboard", rc.pid)
	c.Link = &l
	rc.add(c)
	rc.text("Here's today at a glance.")
	rc.chips("Who arrives today?", "Rooms free Friday?")
	return nil
}

func hintDone(m map[string]any) string {
	done, pend := str(m, "completed"), str(m, "pending")
	if done == "" && pend == "" {
		return ""
	}
	return fmt.Sprintf("%s done · %s pending", orDash(done), orDash(pend))
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func nonEmptyRows(rows []cards.Field) []cards.Field {
	out := rows[:0]
	for _, r := range rows {
		if r.Value != "" {
			out = append(out, r)
		}
	}
	return out
}

func hCompareMetrics(rc *reqCtx, it intents.Intent, s Slots) error {
	metric := strings.ToLower(s.Metric)
	labels := map[string]string{"adr": "ADR", "occupancy": "Occupancy", "revpar": "RevPAR", "revenue": "Revenue"}
	if _, ok := labels[metric]; !ok {
		metric = "adr"
	}
	cur := rc.dayOr(s)
	kpiA, err := rc.get("reservation_get_dashboard_kpi_overview", nil, q("propertyId", rc.pidStr(), "date", cur))
	if err != nil {
		return err
	}
	mA := obj(path(kpiA, metric))
	var prevLabel string
	var vB float64
	var okB bool
	var dispB string
	if s.CompareStart != "" {
		kpiB, err := rc.get("reservation_get_dashboard_kpi_overview", nil, q("propertyId", rc.pidStr(), "date", s.CompareStart))
		if err != nil {
			return err
		}
		mB := obj(path(kpiB, metric))
		vB, okB = num(mB, "value")
		dispB, prevLabel = str(mB, "displayValue", "value"), s.CompareStart
	} else {
		vB, okB = num(mA, "previousValue")
		dispB = str(mA, "displayPreviousValue", "previousValue")
		prevLabel = orDash(str(obj(kpiA), "previousWorkingDate"))
	}
	vA, okA := num(mA, "value")
	cmp := &cards.Comparison{
		Metric:   labels[metric],
		Current:  cards.Period{Label: cur, Display: orDash(str(mA, "displayValue", "value"))},
		Previous: cards.Period{Label: prevLabel, Display: orDash(dispB)},
	}
	if okA {
		cmp.Current.Value = &vA
	}
	if okB {
		cmp.Previous.Value = &vB
	}
	if okA && okB { // deterministic server-side delta
		d := vA - vB
		cmp.Delta = &d
		if vB != 0 {
			pct := d / vB * 100
			cmp.DeltaPct = &pct
		}
	}
	l := cards.Link{Label: "Open Revenue & Occupancy Trend", Route: p(rc.pid, "/dashboard")}
	sub := ""
	if s.StartDate != s.EndDate && s.EndDate != "" {
		sub = "Compared as single business days (range totals are on the dashboard trend)."
	}
	rc.add(cards.Card{Type: cards.TypeComparison, Intent: it.ID, Title: labels[metric] + " comparison", Subtitle: sub, Comparison: cmp, Link: &l})
	rc.text(fmt.Sprintf("%s: %s vs %s.", labels[metric], cmp.Current.Display, cmp.Previous.Display))
	return nil
}

// ---- Reservations ----

func statusOf(m map[string]any) string {
	return str(m, "status", "rezStatus", "reservationStatus", "statusName")
}

func isInHouse(m map[string]any) bool {
	s := strings.ToLower(statusOf(m))
	return strings.Contains(s, "in house") || strings.Contains(s, "in_house") || strings.Contains(s, "inhouse") || strings.Contains(s, "checked in") || strings.Contains(s, "checkedin") || s == "checked_in"
}

func (rc *reqCtx) reservationItems(list []map[string]any, max int, flag func(map[string]any) string) []cards.ListItem {
	names := true // ASI APIs decide what the user can see
	var items []cards.ListItem
	for _, m := range list {
		id := recordID(m)
		title := personName(m)
		if !names || title == "" {
			title = strings.TrimSpace("Room " + orDash(roomLabel(m)) + " " + str(m, "roomTypeName", "roomType"))
		}
		var sub []string
		if names {
			if n := reservationNumber(m); n != "" {
				sub = append(sub, "#"+n)
			}
		}
		if r := roomLabel(m); r != "" && names {
			sub = append(sub, "Room "+r)
		}
		if st := statusOf(m); st != "" {
			sub = append(sub, st)
		}
		item := cards.ListItem{ID: id, Title: title, Subtitle: strings.Join(sub, " · ")}
		if flag != nil {
			if b := flag(m); b != "" {
				item.Badges = []string{b}
			}
		}
		if names {
			item.Link = reservationLink(rc.pid, id)
		}
		items = append(items, item)
		if len(items) >= max {
			break
		}
	}
	return items
}

func hArrivals(rc *reqCtx, it intents.Intent, s Slots) error {
	d := rc.dayOr(s)
	v, err := rc.get("reservation_get_reservations", nil, q("propertyId", rc.pidStr(), "checkInDateStart", d, "checkInDateEnd", d, "pageSize", "50"))
	if err != nil {
		return err
	}
	list := findList(v)
	items := rc.reservationItems(list, 10, nil)
	total, known, note := pageCount(v, len(list), 50, len(items))
	title := fmt.Sprintf("Arrivals %s", d)
	if known {
		title = fmt.Sprintf("Arrivals %s (%d)", d, total)
	}
	link := cards.Link{Label: "Open arrivals", Route: p(rc.pid, "/reservation"), Params: map[string]string{"checkInDateStart": d, "checkInDateEnd": d}}
	rc.add(cards.Card{Type: cards.TypeList, Intent: it.ID, Title: title, Subtitle: note, Items: items, Total: shownTotal(total, known), Link: &link})
	if known {
		rc.text(fmt.Sprintf("%d arrivals on %s.", total, d))
	} else {
		rc.text("Arrivals on " + d + ".")
	}
	rc.chips("Who's departing today?", "Any VIPs arriving?")
	return nil
}

func hDepartures(rc *reqCtx, it intents.Intent, s Slots) error {
	d := rc.dayOr(s)
	v, err := rc.get("reservation_get_reservations", nil, q("propertyId", rc.pidStr(), "checkoutDateStart", d, "checkoutDateEnd", d, "pageSize", "50"))
	if err != nil {
		return err
	}
	list := findList(v)
	still := 0
	for _, m := range list {
		if isInHouse(m) {
			still++
		}
	}
	flag := func(m map[string]any) string {
		if isInHouse(m) {
			return "Still checked in"
		}
		return ""
	}
	items := rc.reservationItems(list, 10, flag)
	total, known, note := pageCount(v, len(list), 50, len(items))
	title := fmt.Sprintf("Departures %s", d)
	if known {
		title = fmt.Sprintf("Departures %s (%d)", d, total)
	}
	sub := fmt.Sprintf("%d still checked in", still)
	if note != "" {
		sub = sub + " · " + note
	}
	link := cards.Link{Label: "Open departures", Route: p(rc.pid, "/reservation"), Params: map[string]string{"checkoutDateStart": d, "checkoutDateEnd": d}}
	rc.add(cards.Card{Type: cards.TypeList, Intent: it.ID, Title: title, Subtitle: sub, Items: items, Total: shownTotal(total, known), Link: &link})
	if known {
		rc.text(fmt.Sprintf("%d departures on %s, %d still checked in.", total, d, still))
	} else {
		rc.text(fmt.Sprintf("Departures on %s, %d still checked in on this page.", d, still))
	}
	return nil
}

func hMissingDates(rc *reqCtx, it intents.Intent, s Slots) error {
	if s.StartDate != "" {
		return hAvailability(rc, it, s)
	}
	d := rc.bizDate
	t, _ := time.Parse("2006-01-02", d)
	fri := t
	for fri.Weekday() != time.Friday {
		fri = fri.AddDate(0, 0, 1)
	}
	f := fri.Format("2006-01-02")
	opt := func(label, from, to string) cards.Option {
		return cards.Option{Label: label, Value: from + ".." + to, Message: fmt.Sprintf("Rooms free from %s to %s", from, to)}
	}
	rc.resp.Intent.ID, rc.resp.Intent.ScenarioID = "check_availability_missing_dates", "G-172"
	rc.add(cards.Clarify("check_availability_missing_dates", "For which dates? 'Free' means something different tonight vs the weekend.",
		opt("Tonight", d, addDays(d, 1)), opt("Tomorrow", addDays(d, 1), addDays(d, 2)),
		opt("This weekend", f, addDays(f, 2)), opt("Next 7 days", d, addDays(d, 7))))
	return nil
}

func hAvailability(rc *reqCtx, it intents.Intent, s Slots) error {
	if s.StartDate == "" && s.Date == "" {
		return hMissingDates(rc, it, s)
	}
	from := s.StartDate
	if from == "" {
		from = s.Date
	}
	to := s.EndDate
	if to == "" || to == from {
		to = addDays(from, 1)
	}
	v, err := rc.get("config_get_room_types_summary", nil, q("propertyId", rc.pidStr(), "startDate", from, "endDate", to, "roomType", s.RoomType, "pageSize", "50"))
	if err != nil {
		return err
	}
	listMaps := findList(v)
	var items []cards.ListItem
	for _, m := range listMaps {
		name := str(m, "roomTypeName", "name", "roomType", "title")
		avail := str(m, "available", "availableRooms", "availableCount", "totalAvailable", "availability")
		rate := str(m, "rate", "baseRate", "minRate", "averageRate", "displayRate", "price")
		sub := orDash(avail) + " available"
		if rate != "" {
			sub += " · rate " + rate
		}
		items = append(items, cards.ListItem{ID: str(m, "roomTypeId", "id"), Title: name, Subtitle: sub})
	}
	link := reservationCreateLink(rc, from, to, s.RoomType, listMaps)
	rc.add(cards.Card{Type: cards.TypeList, Intent: it.ID, Title: fmt.Sprintf("Availability %s → %s", from, to), Items: items, Total: len(items), Link: &link})
	rc.text(fmt.Sprintf("Availability by room type for %s to %s.", from, to))
	return nil
}

// resolveReservation finds exactly one reservation or renders a clarify /
// disambiguation card (returns nil, nil in that case).
func (rc *reqCtx) resolveReservation(s Slots) (map[string]any, error) {
	key := s.Confirmation
	if key == "" {
		key = s.GuestName
	}
	if key == "" {
		key = s.Room
	}
	if key == "" && rc.sess.LastEntity != nil && rc.sess.LastEntity.Kind == "reservation" {
		key = rc.sess.LastEntity.ID
	}
	if key == "" {
		rc.add(cards.Clarify(rc.intent.ID, "Which reservation do you mean? Give me a guest name, room or confirmation number."))
		return nil, nil
	}
	v, err := rc.get("reservation_get_reservations_search", nil, q("propertyId", rc.pidStr(), "key", key, "pageSize", "10"))
	if err != nil {
		return nil, err
	}
	list := findList(v)
	switch {
	case len(list) == 0:
		rc.add(cards.Text(fmt.Sprintf("No reservation matches %q.", key)))
		return nil, nil
	case len(list) > 1:
		var opts []cards.Option
		for _, m := range list {
			n := reservationNumber(m)
			if n == "" {
				n = recordID(m)
			}
			label := strings.TrimSpace(fmt.Sprintf("%s #%s %s→%s", personName(m), n, str(m, "checkIn", "checkInDate", "arrivalDate"), str(m, "checkOut", "checkoutDate", "departureDate")))
			opts = append(opts, cards.Option{Label: label, Value: n, Message: fmt.Sprintf("%s reservation %s", strings.ReplaceAll(rc.intent.ID, "_", " "), n)})
		}
		rc.resp.Intent.Handler = "dedicated"
		rc.add(cards.Card{Type: cards.TypeDisambiguation, Intent: rc.intent.ID, Title: fmt.Sprintf("Several reservations match %q — which one?", key), Options: opts})
		return nil, nil
	}
	m := list[0]
	id := recordID(m)
	if id != "" {
		if det, err := rc.get("reservation_get_reservations_id", map[string]string{"id": id}, q("propertyId", rc.pidStr())); err == nil {
			if dm := obj(det); dm != nil {
				if inner := obj(path(dm, "data")); inner != nil {
					dm = inner
				}
				for k, v := range m {
					if _, ok := dm[k]; !ok {
						dm[k] = v
					}
				}
				m = dm
			}
		}
	}
	n := reservationNumber(m)
	if n == "" {
		n = id
	}
	rc.remember(Entity{Kind: "reservation", ID: n, Label: personName(m)})
	return m, nil
}

func hLookupReservation(rc *reqCtx, it intents.Intent, s Slots) error {
	m, err := rc.resolveReservation(s)
	if err != nil || m == nil {
		return err
	}
	id := recordID(m)
	rows := nonEmptyRows([]cards.Field{
		{Label: "Guest", Value: personName(m)},
		{Label: "Confirmation", Value: reservationNumber(m)},
		{Label: "Status", Value: statusOf(m)},
		{Label: "Arrival", Value: str(m, "checkIn", "checkInDate", "arrivalDate")},
		{Label: "Departure", Value: str(m, "checkOut", "checkoutDate", "departureDate")},
		{Label: "Room", Value: roomLabel(m)},
		{Label: "Room type", Value: str(m, "roomTypeName", "roomType")},
		{Label: "Rate type", Value: str(m, "rateTypeName", "rateType")},
		{Label: "Business source", Value: str(m, "businessSourceName", "businessSource")},
	})
	rc.add(cards.Card{Type: cards.TypeData, Intent: it.ID, Title: "Reservation", Rows: rows, Link: reservationLink(rc.pid, id)})
	rc.text("Here's the reservation.")
	rc.chips("What's the balance?", "Is it ready for check-in?", "What's the cancellation fee?")
	return nil
}

func (rc *reqCtx) paymentSummary(id string) map[string]any {
	if id == "" {
		return nil
	}
	v, err := rc.get("reservation_get_reservations_id_payment_summary", map[string]string{"id": id}, q("propertyId", rc.pidStr()))
	if err != nil {
		return nil
	}
	if d := obj(path(v, "data")); d != nil {
		return d
	}
	return obj(v)
}

func moneyField(m map[string]any, label string, keys ...string) cards.Field {
	if f, ok := num(m, keys...); ok {
		return cards.Field{Label: label, Value: money(f, "")}
	}
	return cards.Field{Label: label}
}

func hPolicyCheck(rc *reqCtx, it intents.Intent, s Slots) error {
	m, err := rc.resolveReservation(s)
	if err != nil || m == nil {
		return err
	}
	polID := deepStr(m, "cancellationPolicyId")
	if polID == "" {
		if rt := deepStr(m, "rateTypeId"); rt != "" {
			if v, err := rc.get("config_get_rate_types_id", map[string]string{"id": rt}, q("propertyId", rc.pidStr())); err == nil {
				polID = deepStr(v, "cancellationPolicyId")
			}
		}
	}
	if polID == "" {
		rc.add(cards.Card{Type: cards.TypeGuide, Intent: it.ID, Title: "No cancellation policy linked", Message: "This reservation's rate type has no cancellation policy Genie can read.", Link: reservationLink(rc.pid, recordID(m))})
		rc.text("I couldn't find a cancellation policy on this reservation.")
		return nil
	}
	pv, err := rc.get("config_get_cancellation_policies_id", map[string]string{"id": polID}, q("propertyId", rc.pidStr()))
	if err != nil {
		return err
	}
	pol := obj(pv)
	if d := obj(path(pol, "data")); d != nil {
		pol = d
	}
	name := orDash(str(pol, "name", "policyName", "title"))
	var steps []string
	if desc := str(pol, "description", "policyText", "text", "details"); desc != "" {
		steps = append(steps, desc)
	}
	for _, r := range scalarRows(pol, 12) {
		if r.Label == "Name" || r.Label == "Description" {
			continue
		}
		steps = append(steps, r.Label+": "+r.Value)
	}
	arrival := str(m, "checkIn", "checkInDate", "arrivalDate")
	sub := ""
	if a, err := time.Parse("2006-01-02", dateRe.FindString(arrival)); err == nil {
		if b, err := time.Parse("2006-01-02", rc.bizDate); err == nil {
			sub = fmt.Sprintf("Arrival %s · %d days from the business date", a.Format("2006-01-02"), int(a.Sub(b).Hours()/24))
		}
	}
	cit := cards.Citation{ID: "cancellation-policy:" + polID, Source: "Policies and Strategies → Cancellation Policies", Title: "Cancellation policy · " + name}
	rc.add(cards.Card{Type: cards.TypeGuide, Intent: it.ID, Title: "Cancellation policy · " + name, Subtitle: sub, Steps: steps,
		Message:   "Fee calculation from policy rules is pending the policy-rule schema; the rules above are what applies.",
		Citations: []cards.Citation{cit}, Link: reservationLink(rc.pid, recordID(m))})
	rc.resp.Citations = append(rc.resp.Citations, cit)
	rc.text("Here's the cancellation policy for that reservation. Source: Cancellation policy · " + name)
	return nil
}

func hFolioBalance(rc *reqCtx, it intents.Intent, s Slots) error {
	m, err := rc.resolveReservation(s)
	if err != nil || m == nil {
		return err
	}
	id := recordID(m)
	ps := rc.paymentSummary(id)
	if ps == nil {
		return errors.New("payment summary unavailable")
	}
	rows := nonEmptyRows([]cards.Field{
		{Label: "Guest", Value: personName(m)},
		{Label: "Room", Value: roomLabel(m)},
		moneyField(ps, "Total charges", "totalCharges", "totalAmount"),
		moneyField(ps, "Payments", "paymentCollected"),
		moneyField(ps, "Deposit", "depositAmount"),
		moneyField(ps, "Balance due", "amountDue"),
		{Label: "Status", Value: str(ps, "amountStatus")},
	})
	c := cards.Card{Type: cards.TypeData, Intent: it.ID, Title: "Folio balance", Rows: rows,
		Link: &cards.Link{Label: "Open guest ledger", Route: p(rc.pid, "/guest-ledger"), Params: map[string]string{"reservationId": id}}}
	if pv, err := rc.get("reservation_get_reservations_id_payments", map[string]string{"id": id}, q("propertyId", rc.pidStr())); err == nil {
		for i, pm := range findList(pv) {
			if i >= 5 {
				break
			}
			amt := ""
			if f, ok := num(pm, "amount", "paymentAmount"); ok {
				amt = money(f, "")
			}
			c.Items = append(c.Items, cards.ListItem{Title: strings.TrimSpace(amt + " " + str(pm, "paymentMethodName", "paymentMethod", "type")), Subtitle: str(pm, "createdAt", "paymentDate", "date")})
		}
	}
	rc.add(c)
	rc.text("Here's the folio balance.")
	return nil
}

func hCheckinReadiness(rc *reqCtx, it intents.Intent, s Slots) error {
	m, err := rc.resolveReservation(s)
	if err != nil || m == nil {
		return err
	}
	id := recordID(m)
	var list []cards.CheckItem
	room := roomLabel(m)
	if room != "" {
		list = append(list, cards.CheckItem{Label: "Room assigned", Status: cards.CheckOK, Detail: "Room " + room})
	} else {
		list = append(list, cards.CheckItem{Label: "Room assigned", Status: cards.CheckWarn, Detail: "No room assigned yet", Link: reservationLink(rc.pid, id)})
	}
	if v, err := rc.get("reservation_get_reservations_id_room_status", map[string]string{"id": id}, nil); err == nil {
		st := deepStr(v, "roomStatusName", "statusName", "status")
		ls := strings.ToLower(st)
		status := cards.CheckWarn
		if strings.Contains(ls, "clean") && !strings.Contains(ls, "dirty") || strings.Contains(ls, "inspect") {
			status = cards.CheckOK
		}
		if st == "" {
			status = cards.CheckUnknown
		}
		list = append(list, cards.CheckItem{Label: "Room status", Status: status, Detail: orDash(st), Link: &cards.Link{Label: "Open room status", Route: p(rc.pid, "/room-space-status-change")}})
	} else {
		list = append(list, cards.CheckItem{Label: "Room status", Status: cards.CheckUnknown})
	}
	if ps := rc.paymentSummary(id); ps != nil {
		auth, _ := num(ps, "authorizedAmount")
		paid, _ := num(ps, "paymentCollected")
		dep, _ := num(ps, "depositAmount")
		if auth > 0 || paid > 0 || dep > 0 {
			list = append(list, cards.CheckItem{Label: "Payment on file", Status: cards.CheckOK, Detail: fmt.Sprintf("Authorized %s · Paid %s", money(auth, ""), money(paid, ""))})
		} else {
			list = append(list, cards.CheckItem{Label: "Payment on file", Status: cards.CheckWarn, Detail: "No authorization or payment yet"})
		}
	} else {
		list = append(list, cards.CheckItem{Label: "Payment on file", Status: cards.CheckUnknown})
	}
	list = append(list, cards.CheckItem{Label: "ID / documents", Status: cards.CheckUnknown, Detail: "Check in the reservation's documents tab"})
	if r := str(m, "remarks", "notes", "specialRequests", "comments"); r != "" {
		list = append(list, cards.CheckItem{Label: "Remarks", Status: cards.CheckWarn, Detail: r})
	}
	l := cards.Link{Label: "Open check-in", Route: p(rc.pid, "/reservation/"+id), Params: map[string]string{"step": "check-in"}}
	rc.add(cards.Card{Type: cards.TypeChecklist, Intent: it.ID, Title: "Check-in readiness · " + orDash(personName(m)), Checklist: list, Link: &l})
	rc.text("I can't check guests in yet, but here's what's ready - check-in is one tap away.")
	return nil
}

func hCheckoutReadiness(rc *reqCtx, it intents.Intent, s Slots) error {
	m, err := rc.resolveReservation(s)
	if err != nil || m == nil {
		return err
	}
	id := recordID(m)
	var list []cards.CheckItem
	if ps := rc.paymentSummary(id); ps != nil {
		due, ok := num(ps, "amountDue")
		switch {
		case !ok:
			list = append(list, cards.CheckItem{Label: "Balance", Status: cards.CheckUnknown})
		case due > 0.005:
			list = append(list, cards.CheckItem{Label: "Balance", Status: cards.CheckFail, Detail: "Outstanding " + money(due, ""), Link: &cards.Link{Label: "Open guest ledger", Route: p(rc.pid, "/guest-ledger")}})
		default:
			list = append(list, cards.CheckItem{Label: "Balance", Status: cards.CheckOK, Detail: "Settled"})
		}
	} else {
		list = append(list, cards.CheckItem{Label: "Balance", Status: cards.CheckUnknown})
	}
	dep := dateRe.FindString(str(m, "checkOut", "checkoutDate", "departureDate"))
	st := cards.CheckOK
	detail := "Departs " + orDash(dep)
	if dep != "" && dep != rc.bizDate {
		st, detail = cards.CheckWarn, "Scheduled departure is "+dep+" (business date "+rc.bizDate+")"
	}
	list = append(list, cards.CheckItem{Label: "Departure date", Status: st, Detail: detail})
	if !isInHouse(m) {
		list = append(list, cards.CheckItem{Label: "In house", Status: cards.CheckWarn, Detail: "Status is " + orDash(statusOf(m))})
	}
	l := cards.Link{Label: "Open check-out", Route: p(rc.pid, "/reservation/"+id), Params: map[string]string{"step": "check-out"}}
	rc.add(cards.Card{Type: cards.TypeChecklist, Intent: it.ID, Title: "Check-out readiness · " + orDash(personName(m)), Checklist: list, Link: &l})
	rc.text("I can't check guests out yet - here's the folio status and the check-out screen.")
	return nil
}

func hLateCheckout(rc *reqCtx, it intents.Intent, s Slots) error {
	c := cards.Card{Type: cards.TypeData, Intent: it.ID, Title: "Late check-out"}
	if v, err := rc.get("config_get_properties_id_global_settings_summary", map[string]string{"id": rc.pidStr()}, nil); err == nil {
		c.Rows = append(c.Rows, cards.Field{Label: "Standard check-out", Value: deepStr(v, "defaultCheckOut")})
	}
	if s.Confirmation != "" || s.GuestName != "" || s.Room != "" || (s.UsesContext && rc.sess.LastEntity != nil) {
		m, err := rc.resolveReservation(s)
		if err != nil {
			return err
		}
		if m == nil {
			return nil
		}
		c.Rows = append(c.Rows, cards.Field{Label: "Guest", Value: personName(m)}, cards.Field{Label: "Departure", Value: str(m, "checkOut", "checkoutDate", "departureDate")}, cards.Field{Label: "Room", Value: roomLabel(m)})
	}
	if v, err := rc.get("reservation_get_reservations", nil, q("propertyId", rc.pidStr(), "lateCheckout", "true", "checkoutDateStart", rc.bizDate, "checkoutDateEnd", rc.bizDate, "pageSize", "1")); err == nil {
		c.Rows = append(c.Rows, cards.Field{Label: "Late check-outs today", Value: strconv.Itoa(findTotal(v, len(findList(v))))})
	}
	c.Rows = nonEmptyRows(c.Rows)
	c.Message = "Late check-out fee rules aren't exposed by an API Genie can read yet, so I won't quote a fee - the Late Check-out screen shows it."
	c.Link = &cards.Link{Label: "Open late check-out", Route: p(rc.pid, "/late-check-out")}
	rc.add(c)
	rc.text("Here's the late check-out info. I won't guess the fee.")
	return nil
}

// ---- Guest ----

func hLookupGuest(rc *reqCtx, it intents.Intent, s Slots) error {
	if gid := parseGuestIDFromLookupMessage(rc.req.Message); gid != "" {
		return rc.renderGuestProfile(it, gid)
	}
	name := s.GuestName
	if name == "" && rc.sess.LastEntity != nil {
		name = rc.sess.LastEntity.Label
	}
	if name == "" {
		rc.add(cards.Clarify(it.ID, "Which guest? Give me a name, email or phone."))
		return nil
	}
	v, err := rc.get("reservation_get_guests", nil, q("propertyId", rc.pidStr(), "searchKey", name, "pageSize", "10"))
	if err != nil {
		return err
	}
	list := dedupeGuestRecords(findList(v))
	if len(list) == 0 {
		rc.add(cards.Text(fmt.Sprintf("No guest profile matches %q.", name)))
		return nil
	}
	if len(list) > 1 {
		opts := guestDisambiguationOptions(list)
		rc.add(cards.Card{Type: cards.TypeDisambiguation, Intent: it.ID, Title: fmt.Sprintf("Several guests match %q — which one?", name), Options: opts})
		return nil
	}
	return rc.renderGuestProfile(it, recordID(list[0]))
}

func (rc *reqCtx) renderGuestProfile(it intents.Intent, gid string) error {
	if gid == "" {
		return errors.New("guest id is required")
	}
	g := map[string]any{}
	if gv, err := rc.get("reservation_get_guests_guest_id", map[string]string{"guestId": gid}, q("propertyId", rc.pidStr())); err == nil {
		if gm := obj(gv); gm != nil {
			if d := obj(path(gm, "data")); d != nil {
				gm = d
			}
			g = gm
		}
	}
	rows := []cards.Field{
		{Label: "Name", Value: personName(g)},
		{Label: "Email", Value: str(g, "email")},
		{Label: "Phone", Value: str(g, "phone", "phoneNumber", "mobile")},
		{Label: "VIP", Value: str(g, "vip", "isVip", "vipLevel")},
		{Label: "Category", Value: str(g, "guestCategoryName", "category")},
		{Label: "Preferences", Value: str(g, "preferences", "notes")},
	}
	if rv, err := rc.get("reservation_get_guests_guest_id_reservations", map[string]string{"guestId": gid}, nil); err == nil {
		rl := findList(rv)
		rows = append(rows, cards.Field{Label: "Stays", Value: strconv.Itoa(findTotal(rv, len(rl)))})
		for _, r := range rl {
			if isInHouse(r) || strings.Contains(strings.ToLower(statusOf(r)), "confirm") {
				rows = append(rows, cards.Field{Label: "Active reservation", Value: "#" + orDash(reservationNumber(r)) + " " + str(r, "checkIn", "checkInDate")})
				break
			}
		}
	}
	rc.remember(Entity{Kind: "guest", ID: gid, Label: personName(g)})
	rc.add(cards.Card{Type: cards.TypeData, Intent: it.ID, Title: "Guest profile", Rows: nonEmptyRows(rows),
		Link: &cards.Link{Label: "Open guest", Route: p(rc.pid, "/guests/"+gid)}})
	rc.text("Here's the guest profile.")
	return nil
}

// ---- Housekeeping ----

func hRoomStatusSummary(rc *reqCtx, it intents.Intent, s Slots) error {
	v, err := rc.get("config_get_room_statuses_summary", nil, q("propertyId", rc.pidStr()))
	if err != nil {
		return err
	}
	c := cards.Card{Type: cards.TypeData, Intent: it.ID, Title: "Room status", Subtitle: str(obj(v), "workingDate")}
	for _, m := range findList(v) {
		c.Rows = append(c.Rows, cards.Field{Label: orDash(str(m, "roomStatusName", "name")), Value: str(m, "count")})
	}
	c.Rows = append(c.Rows, cards.Field{Label: "Total rooms", Value: str(obj(v), "totalRooms")}, cards.Field{Label: "Sellable", Value: str(obj(v), "sellableRooms")})
	c.Rows = nonEmptyRows(c.Rows)
	c.Link = &cards.Link{Label: "Open room status", Route: p(rc.pid, "/room-space-status-change")}
	rc.add(c)
	rc.text("Here are room counts by housekeeping status.")
	return nil
}

func hkSeesAll(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner", "admin", "manager", "supervisor":
		return true
	}
	return false
}

func hkAssignmentItems(rows []map[string]any, role, callerID string) (string, []cards.ListItem) {
	seesAll := hkSeesAll(role)
	title := "Housekeeping assignments"
	if !seesAll {
		title = "My assignments"
	}
	var items []cards.ListItem
	for _, m := range rows {
		user := obj(path(m, "user"))
		uid := str(user, "userId", "id")
		if uid == "" {
			uid = str(m, "userId", "assigneeId", "housekeeperId")
		}
		if !seesAll && (callerID == "" || uid != callerID) {
			continue
		}
		who := personName(user)
		if who == "" {
			who = personName(m)
		}
		var rooms []string
		for _, a := range findList(path(m, "assignments")) {
			if n := str(a, "name"); n != "" {
				rooms = append(rooms, n)
			}
		}
		items = append(items, cards.ListItem{ID: uid, Title: orDash(who), Subtitle: strings.Join(rooms, ", ")})
	}
	return title, items
}

func hHKAssignments(rc *reqCtx, it intents.Intent, s Slots) error {
	v, err := rc.get("config_get_properties_id_hk_assignments", map[string]string{"id": rc.pidStr()}, nil)
	if err != nil {
		return err
	}
	u := rc.currentUser()
	title, items := hkAssignmentItems(findList(v), u.role, u.id)
	rc.add(cards.Card{Type: cards.TypeList, Intent: it.ID, Title: title, Items: items, Total: len(items), Link: &cards.Link{Label: navigateActionGoThere, Route: p(rc.pid, "/room-space-status-change")}})
	rc.text(fmt.Sprintf("%d assignments.", len(items)))
	return nil
}

func shownTotal(total int, known bool) int {
	if !known {
		return 0
	}
	return total
}

func hUpdateRoomStatusNav(rc *reqCtx, it intents.Intent, s Slots) error {
	c := cards.Card{Type: cards.TypeNavigate, Intent: it.ID, Title: "Update room status"}
	if s.Room != "" {
		if v, err := rc.get("config_get_room_space_statuses", nil, q("propertyId", rc.pidStr(), "searchKey", s.Room, "pageSize", "5")); err == nil {
			if l := findList(v); len(l) > 0 {
				c.Rows = nonEmptyRows([]cards.Field{{Label: "Room", Value: orDash(roomLabel(l[0]))}, {Label: "Current status", Value: str(l[0], "roomStatusName", "statusName", "status")}})
			}
		}
		rc.remember(Entity{Kind: "room", ID: s.Room, Label: "Room " + s.Room})
	}
	c.Subtitle = "I can't change room status yet — review below and open the screen."
	c.Link = &cards.Link{Label: navigateActionGoThere, Route: p(rc.pid, "/room-space-status-change"), Params: map[string]string{"room": s.Room}}
	if s.Room == "" {
		c.Link.Params = nil
	}
	rc.add(c)
	return nil
}

// ---- Rates ----

func hRateQuery(rc *reqCtx, it intents.Intent, s Slots) error {
	v, err := rc.get("config_get_rate_types", nil, q("propertyId", rc.pidStr(), "pageSize", "20", "searchKey", s.RoomType))
	if err != nil {
		return err
	}
	var items []cards.ListItem
	for _, m := range findList(v) {
		sub := strings.TrimSpace(str(m, "baseRate", "rate", "amount", "defaultRate") + " " + str(m, "status", "category"))
		items = append(items, cards.ListItem{ID: str(m, "id"), Title: orDash(str(m, "name", "rateTypeName", "shortName")), Subtitle: sub})
	}
	c := cards.Card{Type: cards.TypeList, Intent: it.ID, Title: "Rate types", Items: items, Total: findTotal(v, len(items)), Link: &cards.Link{Label: "Open property settings", Route: p(rc.pid, "/edit")}}
	if s.StartDate != "" {
		if av, err := rc.get("config_get_rate_adjustments", nil, q("propertyId", rc.pidStr(), "startDate", s.StartDate, "endDate", s.EndDate, "pageSize", "1")); err == nil {
			c.Subtitle = fmt.Sprintf("%d rate adjustments %s → %s", findTotal(av, len(findList(av))), s.StartDate, s.EndDate)
		}
	}
	rc.add(c)
	rc.text("Here are the rate types.")
	return nil
}

// ---- Audit ----

func hNightAuditStatus(rc *reqCtx, it intents.Intent, s Slots) error {
	v, err := rc.get("reservation_get_night_audit_status", nil, q("propertyId", rc.pidStr()))
	if err != nil {
		return err
	}
	m := obj(v)
	st := metricValue(path(m, "status"), "name", "status", "value")
	steps := findList(path(m, "steps"))
	done := 0
	for _, sp := range steps {
		if strings.Contains(strings.ToLower(str(sp, "status")), "complete") {
			done++
		}
	}
	rows := nonEmptyRows([]cards.Field{
		{Label: "Status", Value: st},
		{Label: "Business date", Value: str(m, "workingDate")},
		{Label: "Steps completed", Value: fmt.Sprintf("%d of %d", done, len(steps))},
		{Label: "Failure reason", Value: str(m, "failureReason")},
		{Label: "Retries", Value: str(m, "retryCount")},
	})
	rc.add(cards.Card{Type: cards.TypeData, Intent: it.ID, Title: "Night audit status", Rows: rows, Link: &cards.Link{Label: "Open night audit", Route: p(rc.pid, "/night-audit")}})
	rc.text("Night audit status: " + orDash(st) + ".")
	return nil
}

func (rc *reqCtx) countCheck(label, module, feature, tool string, pathP map[string]string, qv url.Values, warnIfPositive bool, link *cards.Link) cards.CheckItem {
	v, err := rc.get(tool, pathP, qv)
	if isAuthErr(err) {
		return cards.CheckItem{Label: label, Status: cards.CheckUnknown, Detail: "Not available for your role"}
	}
	if err != nil {
		return cards.CheckItem{Label: label, Status: cards.CheckUnknown, Detail: "Couldn't read"}
	}
	n := findTotal(v, len(findList(v)))
	st := cards.CheckOK
	if warnIfPositive && n > 0 {
		st = cards.CheckWarn
	}
	return cards.CheckItem{Label: label, Status: st, Detail: strconv.Itoa(n), Link: link}
}

func (rc *reqCtx) nightAuditChecklist() []cards.CheckItem {
	pid, d := rc.pidStr(), rc.bizDate
	manage := &cards.Link{Label: "Open reservations", Route: p(rc.pid, "/reservation")}
	list := []cards.CheckItem{
		rc.countCheck("Unposted OTA reservations", "Reservation", "Reservation", "reservation_get_reservations_properties_property_id_unposted_reservations",
			map[string]string{"propertyId": pid}, q("countOnly", "true"), true, manage),
	}
	// Due-outs still in house (computed from the departures list).
	{
		if v, err := rc.get("reservation_get_reservations", nil, q("propertyId", pid, "checkoutDateStart", d, "checkoutDateEnd", d, "pageSize", "200")); err == nil {
			rows := findList(v)
			n := 0
			for _, m := range rows {
				if isInHouse(m) {
					n++
				}
			}
			_, known, note := pageCount(v, len(rows), 200, len(rows))
			detail := strconv.Itoa(n)
			if note != "" {
				if known {
					detail = fmt.Sprintf("%d · %s", n, note)
				} else {
					detail = note
				}
			}
			st := cards.CheckOK
			if n > 0 {
				st = cards.CheckWarn
			}
			list = append(list, cards.CheckItem{Label: "Due-outs still in house", Status: st, Detail: detail, Link: manage})
		} else {
			detail := "Couldn't read"
			if isAuthErr(err) {
				detail = "Not available for your role"
			}
			list = append(list, cards.CheckItem{Label: "Due-outs still in house", Status: cards.CheckUnknown, Detail: detail})
		}
	}
	list = append(list,
		rc.countCheck("Open action items", "Dashboard", "Action Items", "reservation_get_dashboard_action_items", nil, q("propertyId", pid, "date", d, "pageSize", "1"), true, &cards.Link{Label: "Open dashboard", Route: p(rc.pid, "/dashboard")}),
		rc.countCheck("Hourly stays overlapping the audit", "Audit", "Night Audit", "reservation_get_night_audit_hourly_overlaps", nil, q("propertyId", pid), true, nil),
	)
	{
		if v, err := rc.get("reservation_get_dashboard_today_summary", nil, q("propertyId", pid, "date", d)); err == nil {
			pend := str(obj(path(v, "arrivals")), "pending")
			n, _ := strconv.Atoi(pend)
			st := cards.CheckOK
			if n > 0 {
				st = cards.CheckWarn
			}
			list = append(list, cards.CheckItem{Label: "Arrivals not yet checked in", Status: st, Detail: orDash(pend)})
		}
	}
	return list
}

func hNightAuditReadiness(rc *reqCtx, it intents.Intent, s Slots) error {
	list := rc.nightAuditChecklist()
	warn := 0
	for _, c := range list {
		if c.Status == cards.CheckWarn || c.Status == cards.CheckFail {
			warn++
		}
	}
	rc.add(cards.Card{Type: cards.TypeChecklist, Intent: it.ID, Title: "Night audit readiness · " + rc.bizDate, Checklist: list, Link: &cards.Link{Label: navigateActionGoThere, Route: p(rc.pid, "/night-audit")}})
	if warn == 0 {
		rc.text("Everything I can check looks ready for night audit.")
	} else {
		rc.text(fmt.Sprintf("%d item(s) need attention before night audit.", warn))
	}
	return nil
}

func hNightAuditWalkthrough(rc *reqCtx, it intents.Intent, s Slots) error {
	if err := hNightAuditReadiness(rc, it, s); err != nil {
		return err
	}
	// Readiness checklist already includes the night audit deep link.
	return nil
}

// ---- Reports ----

func hViewReport(rc *reqCtx, it intents.Intent, s Slots) error {
	phrase := strings.TrimSpace(s.Report)
	if phrase == "" && messageRequestsSpecificReport(rc.req.Message) {
		phrase = strings.TrimSpace(rc.req.Message)
	}
	return rc.handleReportDeepLink(it, s, phrase)
}

func hRevenue(rc *reqCtx, it intents.Intent, s Slots) error {
	from, to := revenueWindow(s, rc.bizDate)
	v, err := rc.get("reservation_get_api_v1_reports_sales_and_revenue", nil, q("propertyId", rc.pidStr(), "startDate", from, "endDate", to))
	if err != nil {
		return err
	}
	src := obj(v)
	for _, k := range []string{"totals", "summary", "total", "grandTotal"} {
		if t := obj(path(v, k)); t != nil {
			src = t
			break
		}
		if t := obj(path(v, "data", k)); t != nil {
			src = t
			break
		}
	}
	rows := scalarRows(src, 12)
	rc.add(cards.Card{Type: cards.TypeData, Intent: it.ID, Title: "Sales & revenue", Subtitle: from + " → " + to, Rows: rows,
		Link: &cards.Link{Label: "Open Sales and Revenue Report", Route: p(rc.pid, "/reports"), Params: map[string]string{"report": "sales-and-revenue", "startDate": from, "endDate": to}}})
	rc.text(fmt.Sprintf("Sales and revenue for %s to %s.", from, to))
	return nil
}

// ---- Policies / guides ----

func hPolicyLookup(rc *reqCtx, it intents.Intent, s Slots) error {
	v, err := rc.get("config_get_cancellation_policies", nil, q("propertyId", rc.pidStr(), "pageSize", "20"))
	if err != nil {
		return err
	}
	c := cards.Card{Type: cards.TypeGuide, Intent: it.ID, Title: "Cancellation policies"}
	for _, m := range findList(v) {
		name := orDash(str(m, "name", "policyName"))
		desc := str(m, "description", "policyText", "details")
		c.Steps = append(c.Steps, strings.TrimSpace(name+" - "+desc))
		cit := cards.Citation{ID: "cancellation-policy:" + str(m, "id"), Source: "Policies and Strategies → Cancellation Policies", Title: "Cancellation policy · " + name}
		c.Citations = append(c.Citations, cit)
	}
	rc.resp.Citations = append(rc.resp.Citations, c.Citations...)
	c.Link = &cards.Link{Label: "Open property settings", Route: p(rc.pid, "/edit")}
	rc.add(c)
	rc.text("Here are the property's cancellation policies.")
	return nil
}

func hGuide(rc *reqCtx, it intents.Intent, s Slots) error {
	return rc.e.guideFallback(rc, it)
}

// ---- Genie meta ----

func hCapabilities(rc *reqCtx, it intents.Intent, s Slots) error {
	var items []cards.ListItem
	total := 0
	for _, x := range intents.All() {
		if x.Priority != "P0" || len(x.Examples) == 0 {
			continue
		}
		total++
		if len(items) < 12 {
			items = append(items, cards.ListItem{ID: x.ID, Title: x.Examples[0], Subtitle: x.Module})
		}
	}
	rc.add(cards.Card{Type: cards.TypeList, Intent: it.ID, Title: "Things I can help with", Items: items, Total: total})
	rc.text("Here's what I can do. I answer questions and open screens - I don't make changes yet.")
	for i, x := range items {
		if i >= 3 {
			break
		}
		rc.chips(x.Title)
	}
	return nil
}

func hNavigate(rc *reqCtx, it intents.Intent, s Slots) error {
	dest := s.Destination
	if dest == "" {
		dest = rc.req.Message
	}
	if messageRequestsSpecificReport(dest) {
		return rc.handleReportDeepLink(it, s, dest)
	}
	r, ok := findRoute(dest)
	if !ok {
		rc.add(cards.Clarify(it.ID, "Where would you like to go?",
			cards.Option{Label: "Dashboard", Value: "dashboard", Message: "Open dashboard"},
			cards.Option{Label: "Reservations", Value: "reservations", Message: "Open reservations"},
			cards.Option{Label: "Night audit", Value: "night audit", Message: "Open night audit"}))
		return nil
	}
	params := map[string]string{}
	if s.StartDate != "" {
		params["checkInDate"] = s.StartDate
	}
	if s.EndDate != "" && s.EndDate != s.StartDate {
		params["checkOutDate"] = s.EndDate
	}
	if s.RoomType != "" {
		if _, err := strconv.ParseInt(strings.TrimSpace(s.RoomType), 10, 64); err == nil {
			params["roomTypeId"] = strings.TrimSpace(s.RoomType)
		}
	}
	if len(params) == 0 {
		params = nil
	}
	rc.add(r.navigateCard(it.ID, rc.pid, params))
	return nil
}

func hNavigateEntity(rc *reqCtx, it intents.Intent, s Slots) error {
	m, err := rc.resolveReservation(s)
	if err != nil || m == nil {
		return err
	}
	l := reservationLink(rc.pid, recordID(m))
	if l == nil {
		return errors.New("reservation has no id")
	}
	title := "Reservation · " + orDash(personName(m))
	rc.add(navigateScreen(it.ID, title, navigateActionOpenReservation, *l))
	return nil
}

func hDeniedIntent(rc *reqCtx, it intents.Intent, s Slots) error {
	// Reaching here means the RBAC gate passed (user has KPIs READ).
	if s.CompareStart != "" {
		return hCompareMetrics(rc, it, s)
	}
	return hPropertySnapshot(rc, it, s)
}

func hPCI(rc *reqCtx, it intents.Intent, s Slots) error {
	rc.add(cards.Card{Type: cards.TypeDenied, Intent: it.ID, Title: "Card numbers can't be shared in chat", Message: "Use the card terminal or the payment screen for card payments."})
	rc.text("Please don't type card numbers in chat - use the payment terminal flow.")
	return nil
}

func hContextCarry(rc *reqCtx, it intents.Intent, s Slots) error {
	ent := rc.sess.LastEntity
	if ent == nil {
		rc.add(cards.Clarify(it.ID, "Who do you mean? Give me a name, room or confirmation number."))
		return nil
	}
	s.UsesContext = true
	switch ent.Kind {
	case "guest":
		s.GuestName = ent.Label
		return hLookupGuest(rc, it, s)
	case "room":
		s.Room = ent.ID
		return hUpdateRoomStatusNav(rc, it, s)
	}
	s.Confirmation = ent.ID
	return hLookupReservation(rc, it, s)
}

func hPropertyScope(rc *reqCtx, it intents.Intent, s Slots) error {
	// Unreachable as a question: the property always comes from the screen.
	rc.text(fmt.Sprintf("I'm answering for the property you're on (#%d). Switch property in ASI to ask about another one.", rc.pid))
	return nil
}

func hSinceLastShift(rc *reqCtx, it intents.Intent, s Slots) error {
	pid, d := rc.pidStr(), rc.bizDate
	since := addDays(d, -1)
	if !rc.sess.PrevSeen.IsZero() {
		since = rc.sess.PrevSeen.Format("2006-01-02")
	}
	list := []cards.CheckItem{
		rc.countCheck("New reservations entered today", "Reservation", "Reservation", "reservation_get_reservations", nil, q("propertyId", pid, "entryDate", d, "pageSize", "1"), false, &cards.Link{Label: "Open reservations", Route: p(rc.pid, "/reservation")}),
		rc.countCheck("Room moves", "Reports", "In-House Room Change Report", "reservation_get_api_v1_reports_in_house_room_change", nil, q("propertyId", pid, "startDate", since, "endDate", d), false, nil),
		rc.countCheck("Shift notes", "Dashboard", "Shift Notes", "reservation_get_dashboard_shift_notes", nil, q("propertyId", pid, "startDate", since, "endDate", d, "pageSize", "1"), true, &cards.Link{Label: "Open shift notes", Route: p(rc.pid, "/dashboard/shift-notes")}),
		rc.countCheck("Open action items", "Dashboard", "Action Items", "reservation_get_dashboard_action_items", nil, q("propertyId", pid, "date", d, "pageSize", "1"), true, &cards.Link{Label: "Open dashboard", Route: p(rc.pid, "/dashboard")}),
	}
	{
		if v, err := rc.get("reservation_get_dashboard_today_summary", nil, q("propertyId", pid, "date", d)); err == nil {
			list = append(list, cards.CheckItem{Label: "Cancellations today", Status: cards.CheckOK, Detail: orDash(metricValue(path(v, "cancelled"), "reservationCount"))})
		}
	}
	rc.add(cards.Card{Type: cards.TypeChecklist, Intent: it.ID, Title: "Since your last shift", Subtitle: since + " → " + d, Checklist: list})
	rc.text("Here's what changed since your last shift.")
	return nil
}
