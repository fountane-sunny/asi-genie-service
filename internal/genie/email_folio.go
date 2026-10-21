package genie

import (
	"fmt"
	"log/slog"
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

const (
	emailFolioPendingKind = "email_folio_pending"
	emailFolioSentKind    = "email_folio_sent"
)

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
	if sent, ok := decodeEmailFolioSent(rc.sess.LastEntity); ok && messageAsksFolioEmailStatus(rc.req.Message) {
		rc.text(emailFolioStatusText(sent))
		return nil
	}
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

	resType := folioReservationType(m)
	docTypes := []string{folioDocType(rc.req.Message)}
	if messageWantsAllFolioDocs(rc.req.Message) {
		docTypes = folioDocTypesFor(resType)
	}
	guest := personName(m)
	pending := pendingEmailFolio{
		reservationID: id, docType: strings.Join(docTypes, ","), reservationType: resType,
		recipient: recipient, guestName: guest,
	}
	rc.remember(encodeEmailFolioPending(pending))
	action := "send it?"
	option := "Yes, send it"
	reply := "yes, send it"
	if len(docTypes) > 1 {
		action = "send them?"
		option = "Yes, send them"
		reply = "yes, send them"
	}
	rc.add(cards.Clarify(it.ID,
		fmt.Sprintf("Email %s for %s to %s — %s", folioDocsPhrase(docTypes), guest, recipient, action),
		cards.Option{Label: option, Value: "send", Message: reply},
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

	docTypes := folioDocTypeList(pending.docType)
	sentTypes := make([]string, 0, len(docTypes))
	for _, docType := range docTypes {
		body := map[string]any{
			"reservationId":   pending.reservationID,
			"propertyId":      rc.pidStr(),
			"docType":         docType,
			"reservationType": pending.reservationType,
			"email":           pending.recipient,
		}
		// The reservation service accepts one document per /folio/email call.
		if _, err := rc.post("reservation_post_folio_id_email", nil, body); err != nil {
			slog.Error("failed to email folio document", "err", err, "reservationId", pending.reservationID, "docType", docType)
			if len(sentTypes) > 0 {
				partial := pending
				partial.docType = strings.Join(sentTypes, ",")
				rc.rememberSentEmailFolio(partial)
			}
			return err
		}
		sentTypes = append(sentTypes, docType)
	}
	pending.docType = strings.Join(sentTypes, ",")

	phrase := folioDocsPhrase(sentTypes)
	rc.add(cards.Card{
		Type:    cards.TypeData,
		Intent:  it.ID,
		Title:   "Email sent",
		Message: fmt.Sprintf("Sent %s for %s to %s.", phrase, pending.guestName, pending.recipient),
		Link:    reservationLink(rc.pid, pending.reservationID),
	})
	rc.text(fmt.Sprintf("Sent %s to %s.", phrase, pending.recipient))
	rc.rememberSentEmailFolio(pending)
	return nil
}

func (rc *reqCtx) rememberSentEmailFolio(sent pendingEmailFolio) {
	ent := encodeEmailFolioPending(sent)
	ent.Kind = emailFolioSentKind
	rc.remember(ent)
}

func folioDocTypeList(encoded string) []string {
	parts := strings.Split(encoded, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{"folio"}
	}
	return out
}

func folioDocsPhrase(docTypes []string) string {
	labels := make([]string, 0, len(docTypes))
	for _, docType := range docTypes {
		labels = append(labels, strings.ToLower(folioDocLabel(docType)))
	}
	switch len(labels) {
	case 0:
		return "the folio"
	case 1:
		return "the " + labels[0]
	case 2:
		return "the " + labels[0] + " and " + labels[1]
	default:
		return "the " + strings.Join(labels[:len(labels)-1], ", ") + ", and " + labels[len(labels)-1]
	}
}

func emailFolioStatusText(sent pendingEmailFolio) string {
	sentTypes := folioDocTypeList(sent.docType)
	phrase := folioDocsPhrase(sentTypes)
	if sameDocTypes(sentTypes, folioDocTypesFor(sent.reservationType)) {
		return fmt.Sprintf("Yes. I sent %s for %s to %s.", phrase, sent.guestName, sent.recipient)
	}
	return fmt.Sprintf("No. I sent only %s for %s to %s; the other folio documents were not sent.", phrase, sent.guestName, sent.recipient)
}

func sameDocTypes(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, docType := range left {
		counts[docType]++
	}
	for _, docType := range right {
		counts[docType]--
		if counts[docType] < 0 {
			return false
		}
	}
	return true
}

func decodeEmailFolioSent(ent *Entity) (pendingEmailFolio, bool) {
	if ent == nil || ent.Kind != emailFolioSentKind {
		return pendingEmailFolio{}, false
	}
	copy := *ent
	copy.Kind = emailFolioPendingKind
	return decodeEmailFolioPending(&copy)
}

func messageAsksFolioEmailStatus(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "sent") && (strings.Contains(lower, "folio") || strings.Contains(lower, "all"))
}
