package genie

import (
	"fmt"
	"strings"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
)

// folioActionVerbs are the verbs that, together with a folio document word,
// mark a message as a reservation-document action (download_folio /
// email_folio) rather than a generic question or a report export.
var folioActionVerbs = []string{"download", "export", "print", "email", "mail", "send"}

// folioDocWords name the reservation documents /folio/{id} can generate (see
// asi-frontend DocumentType in apps/web/app/apis/reservations.ts). Checked in
// order: more specific phrases must come before "folio" itself.
var folioDocWords = []string{
	"registration form", "registration card", "reg card", "reg form", "registration",
	"other charge", "other-charge",
	"payment receipt",
	"group status",
	"detailed folio", "detailed",
	"folio",
}

// folioDocTypes maps a matched folioDocWords entry to the doc_type value the
// GET /folio/{id} endpoint expects.
var folioDocTypeByWord = map[string]string{
	"registration form": "registration", "registration card": "registration", "reg card": "registration",
	"reg form": "registration", "registration": "registration",
	"other charge": "other-charges", "other-charge": "other-charges",
	"payment receipt": "payment-receipt",
	"group status":    "group-status-report",
	"detailed folio":  "detailed",
	"detailed":        "detailed",
	"folio":           "folio",
}

// messageWantsFolioAction reports whether message is asking Genie to produce
// (download/print) or send (email) a reservation document.
func messageWantsFolioAction(message string) bool {
	lower := strings.ToLower(message)
	hasVerb := false
	for _, v := range folioActionVerbs {
		if strings.Contains(lower, v) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return false
	}
	for _, w := range folioDocWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// messageWantsAllFolioDocs reports a request for every document on the Print
// Folio menu, such as "email all the folios". A plain "folio" remains the
// summary folio only.
func messageWantsAllFolioDocs(message string) bool {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "notice") {
		return false
	}
	return strings.Contains(lower, "all folio") || strings.Contains(lower, "all the folio") ||
		strings.Contains(lower, "all of the folio") || strings.Contains(lower, "every folio") ||
		strings.Contains(lower, "each folio")
}

// messageWantsEmailFolio reports whether a folio-action message should email
// the document rather than download/print it. "Send the folio" is an email
// request; print, download, and export stay on the download path.
func messageWantsEmailFolio(message string) bool {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "email") || strings.Contains(lower, "e-mail") ||
		strings.Contains(lower, "mail") {
		return true
	}
	if strings.Contains(lower, "print") || strings.Contains(lower, "download") ||
		strings.Contains(lower, "export") {
		return false
	}
	return strings.Contains(lower, "send")
}

// messageWantsDefaultFolioDownload detects a bare print/download continuation.
// The previous folio card established the reservation; in that context the
// standard Folio is the least surprising document to download.
func messageWantsDefaultFolioDownload(message string) bool {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "folio") || strings.Contains(lower, "registration") ||
		strings.Contains(lower, "charge") || strings.Contains(lower, "receipt") ||
		strings.Contains(lower, "status") {
		return false
	}
	return strings.Contains(lower, "print") || strings.Contains(lower, "download")
}

// applyFolioIntent steers print/download/email folio requests onto the
// dedicated handlers. The catalogue still has G-029 folio_document (a navigate
// guide whose UserSees text is a spec note), and the classifier often picks it
// for "print folio" — that must never reach the generic navigate path.
func (e *Engine) applyFolioIntent(it intents.Intent, info IntentInfo, slots Slots, message string) (intents.Intent, IntentInfo, Slots) {
	if !messageWantsFolioAction(message) {
		return it, info, slots
	}
	id := "download_folio"
	if messageWantsEmailFolio(message) {
		id = "email_folio"
	}
	if it.ID == id {
		return it, info, slots
	}
	next, ok := intents.Get(id)
	if !ok {
		return it, info, slots
	}
	info = IntentInfo{
		ID:           next.ID,
		ScenarioID:   next.ScenarioID,
		ResponseType: next.ResponseType,
		Handler:      "dedicated",
		Source:       "rule",
		Confidence:   1,
		Slots:        slots,
	}
	return next, info, slots
}

// folioDocType infers the /folio/{id} doc_type query param from the message.
// Defaults to "folio" when the message names no specific document.
func folioDocType(message string) string {
	if docType, ok := folioDocTypeNamed(message); ok {
		return docType
	}
	return "folio"
}

// folioDocTypeNamed reports whether the message names one document. The word
// "folio" on its own is the generic request, not a choice of the standard folio.
func folioDocTypeNamed(message string) (string, bool) {
	lower := strings.ToLower(message)
	for _, w := range folioDocWords {
		if w == "folio" {
			continue
		}
		if strings.Contains(lower, w) {
			return folioDocTypeByWord[w], true
		}
	}
	return "", false
}

// folioDocTypesFor is the Print Folio menu that GET /folio/{id} can generate.
// Split folio and payment receipt need extra choices on the reservation
// screen, so they are not offered as a single download.
func folioDocTypesFor(reservationType string) []string {
	if reservationType == "group" {
		return []string{"folio", "detailed", "other-charges", "group-status-report"}
	}
	return []string{"registration", "folio", "detailed", "other-charges"}
}

// folioDocLabel is the human-facing name for a doc_type, matching the labels
// asi-frontend shows on the Print Folio / Email Folio menus.
func folioDocLabel(docType string) string {
	switch docType {
	case "detailed":
		return "Detailed Folio"
	case "other-charges":
		return "Other Charges Folio"
	case "registration":
		return "Registration Form"
	case "payment-receipt":
		return "Payment Receipt"
	case "group-status-report":
		return "Group Status Report"
	default:
		return "Folio"
	}
}

// folioReservationType mirrors asi-frontend's getReservationTypeFromTableData
// (apps/web/app/utils/folioPDF.ts): a group parent reports type "GROUP", a
// reservation with a groupReservationId is a child of one, everything else is
// an individual reservation.
func folioReservationType(m map[string]any) string {
	if strings.EqualFold(str(m, "type", "reservationType"), "GROUP") {
		return "group"
	}
	if str(m, "groupReservationId") != "" {
		return "child"
	}
	return "individual"
}

// folioFileName is the suggested download filename; the frontend corrects the
// extension from the response Content-Type, so only the stem matters here.
func folioFileName(docType string, m map[string]any) string {
	name := reservationNumber(m)
	if name == "" {
		name = personName(m)
	}
	if name == "" {
		name = "reservation"
	}
	return fmt.Sprintf("%s - %s.pdf", folioDocLabel(docType), name)
}

// folioPOCEmail is the recipient the Email Folio endpoint defaults to when no
// custom address is given.
func folioPOCEmail(m map[string]any) string {
	return str(m, "pocEmail", "contactEmail", "guestEmail", "email")
}

// hDownloadFolio (G-242): resolve the reservation, infer which document the
// user wants, and hand the frontend the same non-mutating GET /folio/{id}
// endpoint the Print Folio menu uses — Genie never renders or stores the PDF.
func hDownloadFolio(rc *reqCtx, it intents.Intent, s Slots) error {
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
	tool, ok := rc.e.Tools["reservation_get_folio_id"]
	if !ok {
		if open != nil {
			rc.add(navigateScreen(it.ID, "Reservation", navigateActionOpenReservation, *open))
		}
		rc.text("I can't download that from here. Open the reservation and use Print Folio.")
		return nil
	}
	service, path := clientExportPath(tool)
	if service == "" || path == "" {
		if open != nil {
			rc.add(navigateScreen(it.ID, "Reservation", navigateActionOpenReservation, *open))
		}
		rc.text("I can't download that from here. Open the reservation and use Print Folio.")
		return nil
	}
	resType := folioReservationType(m)
	docType, named := folioDocTypeNamed(rc.req.Message)
	docTypes := folioDocTypesFor(resType)
	defaultFolio := !named && messageWantsDefaultFolioDownload(rc.req.Message)
	if named || defaultFolio {
		if defaultFolio {
			docType = "folio"
		}
		docTypes = []string{docType}
	}
	downloads := make([]cards.Download, 0, len(docTypes))
	for _, dt := range docTypes {
		label := folioDocLabel(dt)
		if named || defaultFolio {
			label = "Download PDF"
		}
		downloads = append(downloads, cards.Download{
			Label:    label,
			FileName: folioFileName(dt, m),
			Service:  service,
			Path:     strings.ReplaceAll(path, "{id}", id),
			Query: map[string]string{
				"propertyId":       rc.pidStr(),
				"doc_type":         dt,
				"reservation_type": resType,
			},
		})
	}
	title := "Folio"
	message := "Which document do you want?"
	who := personName(m)
	if who == "" {
		who = "this reservation"
	}
	lead := fmt.Sprintf("Here are the folios for %s.", who)
	if named || defaultFolio {
		title = folioDocLabel(docType)
		message = "Here's your download."
		lead = fmt.Sprintf("Here's the %s for %s.", strings.ToLower(folioDocLabel(docType)), who)
	}
	card := cards.Card{
		Type:      cards.TypeData,
		Intent:    it.ID,
		Title:     title,
		Subtitle:  personName(m),
		Message:   message,
		Downloads: downloads,
	}
	if open != nil {
		open.Label = navigateActionOpenReservation
		card.Link = open
	}
	rc.add(card)
	rc.text(lead)
	return nil
}
