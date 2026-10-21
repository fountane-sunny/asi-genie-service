package genie

import (
	"fmt"
	"strconv"
	"strings"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

func hCreateReservation(rc *reqCtx, it intents.Intent, s Slots) error {
	if s.StartDate == "" && s.Date == "" {
		return hCreateReservationSlots(rc, it, s)
	}
	if err := hAvailability(rc, it, s); err != nil {
		return err
	}
	rc.text("I can't book from here - check availability above, then open New Reservation (dates prefilled) to finish.")
	return nil
}

func hCreateReservationSlots(rc *reqCtx, it intents.Intent, s Slots) error {
	guest := s.GuestName
	if guest == "" && rc.sess.LastEntity != nil && rc.sess.LastEntity.Kind == "guest" {
		guest = rc.sess.LastEntity.Label
	}
	from := s.StartDate
	if from == "" {
		from = s.Date
	}
	to := s.EndDate
	var missing []string
	if guest == "" {
		missing = append(missing, "guest name")
	}
	if from == "" {
		missing = append(missing, "check-in date")
	}
	if to == "" && from != "" {
		to = addDays(from, 1) // default one night; stated in the reply
	}
	if from == "" {
		missing = append(missing, "check-out date")
	}
	if s.RoomType == "" {
		missing = append(missing, "room type")
	}
	roomTypeID := ""
	if s.RoomType != "" {
		if _, err := strconv.ParseInt(strings.TrimSpace(s.RoomType), 10, 64); err == nil {
			roomTypeID = strings.TrimSpace(s.RoomType)
		}
	}
	link := reservationCreateLink(rc, from, to, s.RoomType, nil)
	if roomTypeID != "" {
		link.Params = reservationCreatePrefillParams(from, to, roomTypeID, guestIDFromSession(rc))
	}
	if len(missing) == 0 {
		if err := hAvailability(rc, it, s); err != nil {
			return err
		}
		if n := len(rc.resp.Cards); n > 0 && rc.resp.Cards[n-1].Link != nil {
			link = *rc.resp.Cards[n-1].Link
		}
		link.Label = navigateActionGoThere
		rc.add(cards.Card{Type: cards.TypeNavigate, Intent: it.ID, Title: "New reservation for " + guest, Subtitle: "Prefilled — review and save in New Reservation.", Link: &link})
		return nil
	}
	d := rc.bizDate
	var opts []cards.Option
	if from == "" {
		mk := func(label, a, b string) cards.Option {
			return cards.Option{Label: label, Value: a + ".." + b, Message: strings.TrimSpace(fmt.Sprintf("Book a room for %s from %s to %s %s", guest, a, b, s.RoomType))}
		}
		opts = append(opts, mk("Tonight", d, addDays(d, 1)), mk("Tomorrow", addDays(d, 1), addDays(d, 2)), mk("2 nights from today", d, addDays(d, 2)))
	}
	c := cards.Clarify(it.ID, "To prefill the booking I still need: "+strings.Join(missing, ", ")+".", opts...)
	c.Rows = nonEmptyRows([]cards.Field{{Label: "Guest", Value: guest}, {Label: "Check-in", Value: from}, {Label: "Check-out", Value: to}, {Label: "Room type", Value: s.RoomType}})
	c.Link = &link
	rc.add(c)
	return nil
}

func hLogServiceRequest(rc *reqCtx, it intents.Intent, s Slots) error {
	c := cards.Card{Type: cards.TypeNavigate, Intent: it.ID, Title: "New maintenance request", Subtitle: "Prefilled - review and submit it yourself."}
	params := map[string]string{"type": "Maintenance", "issue": truncateStr(rc.req.Message, 200)}
	if s.Room != "" {
		params["room"] = s.Room
		c.Rows = append(c.Rows, cards.Field{Label: "Room", Value: s.Room})
		if v, err := rc.get("config_get_room_space_statuses", nil, q("propertyId", rc.pidStr(), "searchKey", s.Room, "pageSize", "5")); err == nil {
			if l := findList(v); len(l) > 0 {
				c.Rows = append(c.Rows, cards.Field{Label: "Current status", Value: str(l[0], "roomStatusName", "statusName", "status")})
			}
		}
		rc.remember(Entity{Kind: "room", ID: s.Room, Label: "Room " + s.Room})
	}
	c.Rows = nonEmptyRows(append(c.Rows, cards.Field{Label: "Type", Value: "Maintenance"}, cards.Field{Label: "Issue", Value: truncateStr(rc.req.Message, 200)}))
	// Route assumed: no tasks constant in asi-frontend ROUTES; housekeeping screen hosts tasks.
	c.Link = &cards.Link{Label: navigateActionGoThere, Route: p(rc.pid, "/room-space-status-change"), Params: params}
	rc.add(c)
	return nil
}

func hClarifySlot(rc *reqCtx, it intents.Intent, s Slots) error {
	d := s.StartDate
	if d == "" {
		d = s.Date
	}
	biz := rc.bizDate
	if d != "" && biz != "" && d >= biz {
		return hAvailability(rc, it, s)
	}
	question := "Which date do you mean?"
	if d != "" {
		question = fmt.Sprintf("%s is before the business date (%s). Which date do you mean?", d, biz)
	}
	var opts []cards.Option
	for _, o := range []struct {
		label string
		n     int
	}{{"Today", 0}, {"Tomorrow", 1}, {"In 7 days", 7}} {
		day := addDays(biz, o.n)
		opts = append(opts, cards.Option{Label: o.label + " (" + day + ")", Value: day, Message: "Rooms free on " + day})
	}
	if d != "" && len(d) == 10 {
		// same day next month, a common meaning of "the 12th" once it has passed
		if nd := addMonth(d); nd > biz {
			opts = append(opts, cards.Option{Label: nd, Value: nd, Message: "Rooms free on " + nd})
		}
	}
	rc.add(cards.Clarify(it.ID, question, opts...))
	return nil
}

func addMonth(d string) string {
	var y, m, day int
	if _, err := fmt.Sscanf(d, "%d-%d-%d", &y, &m, &day); err != nil {
		return ""
	}
	m++
	if m > 12 {
		m, y = 1, y+1
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, m, day)
}

func hLateCheckoutFeeTier(rc *reqCtx, it intents.Intent, s Slots) error {
	if err := hLateCheckout(rc, it, s); err != nil {
		return err
	}
	if len(rc.resp.Cards) > 0 && rc.resp.Cards[len(rc.resp.Cards)-1].Type == cards.TypeDisambiguation {
		return nil
	}
	who := s.Room
	if who == "" {
		who = s.Confirmation
	}
	if who == "" {
		who = s.GuestName
	}
	opt := func(label string) cards.Option {
		return cards.Option{Label: label, Value: label, Message: strings.TrimSpace(fmt.Sprintf("Late checkout fee %s %s", who, label))}
	}
	rc.add(cards.Clarify(it.ID, "Which late check-out tier do you want quoted? Posting the charge stays on the Late Check-out screen.", opt("until 14:00"), opt("until 18:00"), opt("full night")))
	return nil
}
