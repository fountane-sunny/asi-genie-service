package genie

import (
	"fmt"
	"strings"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

// pendingEmailFolio is the confirm-card payload for G-243, round-tripped
// through rc.sess.LastEntity (Kind "email_folio_pending") between the "I'll
// email X to Y — send it?" turn and the user's yes/no reply. This is Genie's
// first write capability: nothing is ever sent without this explicit confirm.
type pendingEmailFolio struct {
	reservationID   string
	docType         string
	reservationType string
	recipient       string
	guestName       string
}

const emailFolioPendingKind = "email_folio_pending"

func encodeEmailFolioPending(p pendingEmailFolio) Entity {
	return Entity{
		Kind:  emailFolioPendingKind,
		ID:    strings.Join([]string{p.reservationID, p.docType, p.reservationType, p.recipient}, "|"),
		Label: p.guestName,
	}
}

func decodeEmailFolioPending(ent *Entity) (pendingEmailFolio, bool) {
	if ent == nil || ent.Kind != emailFolioPendingKind {
		return pendingEmailFolio{}, false
	}
	parts := strings.Split(ent.ID, "|")
	if len(parts) != 4 {
		return pendingEmailFolio{}, false
	}
	return pendingEmailFolio{
		reservationID: parts[0], docType: parts[1], reservationType: parts[2], recipient: parts[3],
		guestName: ent.Label,
	}, true
}

var affirmativeReplies = []string{
	"yes", "yeah", "yep", "yup", "sure", "confirm", "confirmed",
	"send it", "send", "go ahead", "do it", "please send", "ok", "okay",
}

var negativeReplies = []string{
	"no", "nope", "don't", "do not", "cancel", "stop", "nevermind", "never mind", "skip",
}

// isAffirmative requires the reply to clearly be a yes (whole message or a
// short "yes, ..." prefix) so an unrelated short reply never triggers a send.
func isAffirmative(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	for _, w := range affirmativeReplies {
		if lower == w || strings.HasPrefix(lower, w+" ") || strings.HasPrefix(lower, w+",") {
			return true
		}
	}
	return false
}

func isNegative(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	for _, w := range negativeReplies {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// hEmailFolio (G-243): resolve the reservation, document and recipient, then
// ask for an explicit confirm before ever calling the write endpoint. Genie
// never writes to ASI except through this one confirmed path.
func hEmailFolio(rc *reqCtx, it intents.Intent, s Slots) error {
	if pending, ok := decodeEmailFolioPending(rc.sess.LastEntity); ok {
		return rc.resolveEmailFolioConfirmation(it, pending)
	}

	m, err := rc.resolveReservation(s)
	if err != nil || m == nil {
		return err
	}
	id := recordID(m)
	open := reservationLink(rc.pid, id)
	if id == "" {
		rc.text("I found the reservation but couldn't read its ID.")
		return nil
	}
	recipient := folioPOCEmail(m)
	if recipient == "" {
		if open != nil {
			rc.add(navigateScreen(it.ID, "Reservation", navigateActionOpenReservation, *open))
		}
		rc.text("This reservation has no point-of-contact email on file, so I can't send it. Add one, or open the reservation and use Email Folio with a custom address.")
		return nil
	}

	docType := folioDocType(rc.req.Message)
	guest := personName(m)
	pending := pendingEmailFolio{
		reservationID: id, docType: docType, reservationType: folioReservationType(m),
		recipient: recipient, guestName: guest,
	}
	rc.remember(encodeEmailFolioPending(pending))
	rc.add(cards.Clarify(it.ID,
		fmt.Sprintf("Email the %s for %s to %s — send it?", strings.ToLower(folioDocLabel(docType)), guest, recipient),
		cards.Option{Label: "Yes, send it", Value: "send", Message: "yes, send it"},
		cards.Option{Label: "Cancel", Value: "cancel", Message: "cancel"},
	))
	return nil
}

// resolveEmailFolioConfirmation runs on the turn after the confirm card. Any
// reply that isn't a clear "yes" (including an unrelated message) cancels, so
// the email is sent on an explicit, unambiguous confirm only.
func (rc *reqCtx) resolveEmailFolioConfirmation(it intents.Intent, pending pendingEmailFolio) error {
	rc.remember(Entity{}) // one confirm per ask: clear it either way so a later "yes" can't resend.
	if isNegative(rc.req.Message) || !isAffirmative(rc.req.Message) {
		rc.text("Okay, I won't send it.")
		return nil
	}

	body := map[string]any{
		"reservationId":   pending.reservationID,
		"propertyId":      rc.pidStr(),
		"docType":         pending.docType,
		"reservationType": pending.reservationType,
		"email":           pending.recipient,
	}
	if _, err := rc.post("reservation_post_folio_id_email", map[string]string{"id": pending.reservationID}, body); err != nil {
		return err
	}

	label := strings.ToLower(folioDocLabel(pending.docType))
	rc.add(cards.Card{
		Type:    cards.TypeData,
		Intent:  it.ID,
		Title:   "Email sent",
		Message: fmt.Sprintf("Sent the %s for %s to %s.", label, pending.guestName, pending.recipient),
		Link:    reservationLink(rc.pid, pending.reservationID),
	})
	rc.text(fmt.Sprintf("Sent the %s to %s.", label, pending.recipient))
	return nil
}
