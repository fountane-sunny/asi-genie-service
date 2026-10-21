package genie

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
	genillm "asi-genie-poc/llm"
)

type pendingEmailFolio struct {
	reservationID     string
	docType           string
	reservationType   string
	recipient         string
	guestName         string
	reservationNumber string
	sentAt            time.Time
	sentBy            string
}

const (
	emailFolioPendingKind = "email_folio_pending"
	emailFolioSentKind    = "email_folio_sent"
)

func encodeEmailFolioPending(p pendingEmailFolio) Entity {
	parts := []string{p.reservationID, p.docType, p.reservationType, p.recipient}
	// Older pending rows omit these fields and must still decode.
	if p.reservationNumber != "" || !p.sentAt.IsZero() || p.sentBy != "" {
		at := ""
		if !p.sentAt.IsZero() {
			at = p.sentAt.UTC().Format(time.RFC3339)
		}
		parts = append(parts, p.reservationNumber, at, strings.ReplaceAll(p.sentBy, "|", " "))
	}
	return Entity{
		Kind:  emailFolioPendingKind,
		ID:    strings.Join(parts, "|"),
		Label: p.guestName,
	}
}

func decodeEmailFolioPending(ent *Entity) (pendingEmailFolio, bool) {
	if ent == nil || ent.Kind != emailFolioPendingKind {
		return pendingEmailFolio{}, false
	}
	parts := strings.Split(ent.ID, "|")
	if len(parts) < 4 {
		return pendingEmailFolio{}, false
	}
	p := pendingEmailFolio{
		reservationID: parts[0], docType: parts[1], reservationType: parts[2], recipient: parts[3],
		guestName: ent.Label,
	}
	if len(parts) > 4 {
		p.reservationNumber = parts[4]
	}
	if len(parts) > 5 && parts[5] != "" {
		at, err := time.Parse(time.RFC3339, parts[5])
		if err != nil {
			slog.Warn("email folio sent time is invalid", "err", err)
		} else {
			p.sentAt = at
		}
	}
	if len(parts) > 6 {
		p.sentBy = parts[6]
	}
	return p, true
}

var affirmativeReplies = []string{
	"yes", "yeah", "yep", "yup", "sure", "confirm", "confirmed",
	"send it", "send", "go ahead", "do it", "please send", "ok", "okay",
}

var negativeReplies = []string{
	"no", "nope", "don't", "do not", "cancel", "stop", "nevermind", "never mind", "skip",
}

// Whole message or a "yes, ..." prefix, so an unrelated sentence cannot confirm a send.
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

func hEmailFolio(rc *reqCtx, it intents.Intent, s Slots) error {
	rc.restoreEmailFolioEntity()
	if sent, ok := decodeEmailFolioSent(rc.sess.LastEntity); ok && messageAsksFolioEmailStatus(rc.req.Message) {
		rc.text(emailFolioStatusText(sent))
		return nil
	}
	if pending, ok := decodeEmailFolioPending(rc.sess.LastEntity); ok {
		return rc.resolveEmailFolioConfirmation(it, pending)
	}
	if isEmailFolioConfirmReply(rc.req.Message) {
		// A second "yes" after a successful send must not email the folio again.
		if isAffirmative(rc.req.Message) && !messageWantsResend(rc.req.Message) {
			if sent, ok := rc.recentSentFolio(); ok && sentCoversConfirm(sent, rc.req.History) {
				rc.tellFolioAlreadySent(it, sent)
				return nil
			}
		}
		if hint, ok := emailConfirmFromHistory(rc.req.History); ok {
			return rc.sendConfirmedFolioEmail(it, s, hint)
		}
	}

	// resolveReservation replaces LastEntity, so capture the sent record first.
	sentAlready, alreadySent := rc.recentSentFolio()
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
	number := reservationNumber(m)
	if alreadySent && !messageWantsResend(rc.req.Message) && sameFolioSend(sentAlready, id, number, guest) {
		rc.tellFolioAlreadySent(it, sentAlready)
		return nil
	}
	pending := pendingEmailFolio{
		reservationID: id, docType: strings.Join(docTypes, ","), reservationType: resType,
		recipient: recipient, guestName: guest, reservationNumber: number,
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
	asked := fmt.Sprintf("Email %s for %s to %s — %s", folioDocsPhrase(docTypes), guest, recipient, action)
	rc.add(cards.Clarify(it.ID, asked,
		cards.Option{Label: option, Value: "send", Message: reply},
		cards.Option{Label: "Cancel", Value: "cancel", Message: "cancel"},
	))
	rc.text(asked)
	return nil
}

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
	if sent.sentAt.IsZero() {
		sent.sentAt = time.Now().UTC().Truncate(time.Second)
	}
	if strings.TrimSpace(sent.sentBy) == "" {
		sent.sentBy = strings.TrimSpace(rc.currentUser().name)
	}
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
	var base string
	if sameDocTypes(sentTypes, folioDocTypesFor(sent.reservationType)) {
		base = fmt.Sprintf("Yes. I sent %s for %s to %s.", phrase, sent.guestName, sent.recipient)
	} else {
		base = fmt.Sprintf("No. I sent only %s for %s to %s; the other folio documents were not sent.", phrase, sent.guestName, sent.recipient)
	}
	if facts := sentFolioFacts(sent); facts != "" {
		return base + " " + facts
	}
	return base
}

func sentFolioFacts(sent pendingEmailFolio) string {
	var bits []string
	if number := strings.TrimSpace(sent.reservationNumber); number != "" {
		bits = append(bits, "reservation "+number)
	}
	if !sent.sentAt.IsZero() {
		bits = append(bits, "sent at "+sent.sentAt.UTC().Format("Jan 2, 2006 3:04 PM")+" UTC")
	}
	if name := strings.TrimSpace(sent.sentBy); name != "" {
		bits = append(bits, "by "+name)
	}
	if len(bits) == 0 {
		return ""
	}
	return "That was " + strings.Join(bits, ", ") + "."
}

func alreadySentFolioText(sent pendingEmailFolio) string {
	phrase := folioDocsPhrase(folioDocTypeList(sent.docType))
	text := fmt.Sprintf("I already sent %s for %s to %s.", phrase, sent.guestName, sent.recipient)
	if facts := sentFolioFacts(sent); facts != "" {
		return text + " " + facts
	}
	return text
}

func (rc *reqCtx) tellFolioAlreadySent(it intents.Intent, sent pendingEmailFolio) {
	text := alreadySentFolioText(sent)
	rc.add(cards.Card{
		Type:    cards.TypeData,
		Intent:  it.ID,
		Title:   "Folio already sent",
		Message: text,
		Link:    reservationLink(rc.pid, sent.reservationID),
	})
	rc.text(text)
	ent := encodeEmailFolioPending(sent)
	ent.Kind = emailFolioSentKind
	rc.remember(ent)
}

func messageWantsResend(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "again") || strings.Contains(lower, "resend")
}

func sameFolioSend(sent pendingEmailFolio, reservationID, reservationNo, guest string) bool {
	if sent.reservationID != "" && reservationID != "" {
		return sent.reservationID == reservationID
	}
	if sent.reservationNumber != "" && reservationNo != "" {
		return strings.EqualFold(sent.reservationNumber, reservationNo)
	}
	return guest != "" && strings.EqualFold(strings.TrimSpace(sent.guestName), strings.TrimSpace(guest))
}

func sentCoversConfirm(sent pendingEmailFolio, history []genillm.Message) bool {
	hint, ok := emailConfirmFromHistory(history)
	if !ok {
		return true
	}
	if sent.guestName != "" && strings.EqualFold(sent.guestName, hint.guest) {
		return true
	}
	return sent.recipient != "" && strings.EqualFold(sent.recipient, hint.email)
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

func isEmailFolioConfirmReply(message string) bool {
	lower := strings.TrimRight(strings.ToLower(strings.TrimSpace(message)), "?.!")
	if lower == "send it" || lower == "send them" {
		return true
	}
	return isAffirmative(message) || isNegative(message)
}

// The confirm card is stored with the conversation, so a later "yes" still works after the in-memory session is gone.
func (rc *reqCtx) restoreEmailFolioEntity() bool {
	if _, ok := decodeEmailFolioPending(rc.sess.LastEntity); ok {
		return true
	}
	if _, ok := decodeEmailFolioSent(rc.sess.LastEntity); ok {
		return true
	}
	ent, ok := rc.emailFolioEntityFromHistory()
	if !ok {
		return false
	}
	rc.remember(ent)
	return true
}

// Older sent rows have no timestamp; the saved message time fills it in.
func (rc *reqCtx) recentSentFolio() (pendingEmailFolio, bool) {
	if sent, ok := decodeEmailFolioSent(rc.sess.LastEntity); ok {
		return sent, true
	}
	if rc.e == nil || rc.e.Store == nil || rc.req.SessionID == "" {
		return pendingEmailFolio{}, false
	}
	u := rc.currentUser()
	msgs, err := rc.e.Store.History(rc.ctx, rc.req.SessionID, u.id, rc.pid, 8)
	if err != nil {
		slog.Warn("sent folio lookup failed", "err", err, "propertyId", rc.pid)
		return pendingEmailFolio{}, false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != genillm.RoleAssistant {
			continue
		}
		ent, ok := pendingEmailFromMeta(msgs[i].ToolCalls)
		if !ok {
			continue
		}
		sent, ok := decodeEmailFolioSent(&ent)
		if !ok {
			continue
		}
		if sent.sentAt.IsZero() {
			sent.sentAt = msgs[i].CreatedAt
		}
		return sent, true
	}
	return pendingEmailFolio{}, false
}

func (rc *reqCtx) emailFolioEntityFromHistory() (Entity, bool) {
	if rc.e == nil || rc.e.Store == nil || rc.req.SessionID == "" {
		return Entity{}, false
	}
	u := rc.currentUser()
	msgs, err := rc.e.Store.History(rc.ctx, rc.req.SessionID, u.id, rc.pid, 6)
	if err != nil || len(msgs) == 0 {
		if err != nil {
			slog.Warn("email folio confirm lookup failed", "err", err, "propertyId", rc.pid)
		}
		return Entity{}, false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != genillm.RoleAssistant {
			continue
		}
		if ent, ok := pendingEmailFromMeta(msgs[i].ToolCalls); ok {
			return ent, true
		}
	}
	return Entity{}, false
}

func pendingEmailFromMeta(raw json.RawMessage) (Entity, bool) {
	if len(raw) == 0 {
		return Entity{}, false
	}
	var meta struct {
		PendingEmail *Entity `json:"pendingEmail"`
	}
	if json.Unmarshal(raw, &meta) != nil || meta.PendingEmail == nil {
		return Entity{}, false
	}
	ent := *meta.PendingEmail
	if ent.Kind != emailFolioPendingKind && ent.Kind != emailFolioSentKind {
		return Entity{}, false
	}
	check := ent
	check.Kind = emailFolioPendingKind
	if _, ok := decodeEmailFolioPending(&check); !ok {
		return Entity{}, false
	}
	return ent, true
}

var emailConfirmTitleRe = regexp.MustCompile(`(?i)email (.+) for (.+) to (\S+@\S+)\s+[—-]\s+send (it|them)\??`)

type emailConfirmHint struct {
	guest   string
	email   string
	docType string
	all     bool
}

func emailConfirmFromHistory(messages []genillm.Message) (emailConfirmHint, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != genillm.RoleAssistant {
			continue
		}
		match := emailConfirmTitleRe.FindStringSubmatch(messages[i].Content)
		if match == nil {
			continue
		}
		return emailConfirmHint{
			guest: strings.TrimSpace(match[2]), email: strings.TrimRight(match[3], ".,)"),
			docType: folioDocType(match[1]), all: strings.EqualFold(match[4], "them"),
		}, true
	}
	return emailConfirmHint{}, false
}

func (rc *reqCtx) sendConfirmedFolioEmail(it intents.Intent, s Slots, hint emailConfirmHint) error {
	if isNegative(rc.req.Message) || !isAffirmative(rc.req.Message) {
		rc.text("Okay, I won't send it.")
		return nil
	}
	if s.GuestName == "" {
		s.GuestName = hint.guest
	}
	m, err := rc.resolveReservation(s)
	if err != nil || m == nil {
		return err
	}
	id := recordID(m)
	if id == "" {
		rc.text("I found the reservation but couldn't read its ID.")
		return nil
	}
	docType := hint.docType
	if hint.all {
		docType = strings.Join(folioDocTypesFor(folioReservationType(m)), ",")
	}
	pending := pendingEmailFolio{
		reservationID: id, docType: docType, reservationType: folioReservationType(m),
		recipient: hint.email, guestName: hint.guest, reservationNumber: reservationNumber(m),
	}
	return rc.resolveEmailFolioConfirmation(it, pending)
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
