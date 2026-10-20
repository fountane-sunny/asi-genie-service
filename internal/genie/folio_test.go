package genie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
	"asi-genie-poc/toolregistry/generated"
)

const (
	testReservationJSON      = `{"id":"res-1","reservationNumber":"4471","guestName":"John Smith","pocEmail":"poc@example.com","type":"INDIVIDUAL"}`
	testReservationNoPOCJSON = `{"id":"res-1","reservationNumber":"4471","guestName":"John Smith","type":"INDIVIDUAL"}`
)

// folioFixture serves fake reservation-service reads (search + by id) and
// records any POST /folio/{id}/email body, for download_folio / email_folio
// handler tests. reservationJSON backs both the search result and the detail
// lookup resolveReservation makes once it finds exactly one match.
func folioFixture(t *testing.T, reservationJSON string, posted *[]map[string]any) *reqCtx {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/reservations/search":
			_, _ = w.Write([]byte(`{"data":[` + reservationJSON + `]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/reservations/res-1":
			_, _ = w.Write([]byte(`{"data":` + reservationJSON + `}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/folio/res-1/email":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if posted != nil {
				*posted = append(*posted, body)
			}
			_, _ = w.Write([]byte(`{"message":"Email sent successfully"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	e := New(nil, logOnlyStore{})
	for _, s := range []generated.Service{generated.AuthService, generated.ConfigService, generated.ReservationService} {
		e.HTTP.BaseURLs[s.Name] = srv.URL
	}
	return &reqCtx{
		e: e, ctx: context.Background(), token: "tok", pid: 42,
		sess: &session{PropertyID: 42},
		req:  Request{PropertyID: 42},
		resp: &Response{},
	}
}

func TestMessageWantsFolioAction(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"download the folio for Smith":               true,
		"email the registration form for room 204":   true,
		"print the detailed folio for booking #4471": true,
		"open folio notices":                         false, // no action verb
		"what's the balance on this folio":           false, // no action verb
		"download today's sales and revenue report":  false, // report, not a reservation doc
	}
	for msg, want := range cases {
		if got := messageWantsFolioAction(msg); got != want {
			t.Errorf("messageWantsFolioAction(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestFolioDocTypeInference(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"download the folio for Smith":                  "folio",
		"download the detailed folio for booking #4471": "detailed",
		"email the registration form for room 204":      "registration",
		"download the other charges folio for 305":      "other-charges",
		"email the payment receipt to the guest":        "payment-receipt",
		"download the group status report":              "group-status-report",
	}
	for msg, want := range cases {
		if got := folioDocType(msg); got != want {
			t.Errorf("folioDocType(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestFolioReservationType(t *testing.T) {
	t.Parallel()
	if got := folioReservationType(map[string]any{"type": "GROUP"}); got != "group" {
		t.Errorf("group reservation type = %q", got)
	}
	if got := folioReservationType(map[string]any{"groupReservationId": "g-1"}); got != "child" {
		t.Errorf("child reservation type = %q", got)
	}
	if got := folioReservationType(map[string]any{}); got != "individual" {
		t.Errorf("default reservation type = %q", got)
	}
}

// Folio actions must never be hijacked into download_report (which only
// knows the Reports catalogue) or sent to the generic read-only planner
// (which has no download/confirm card and no write tool).
func TestApplyDownloadIntentSkipsFolioMessages(t *testing.T) {
	t.Parallel()
	it, info, slots := (&Engine{}).applyDownloadIntent(
		intents.Intent{ID: "download_folio"},
		IntentInfo{ID: "download_folio", Source: "llm"},
		Slots{GuestName: "Smith"},
		"download the folio for Smith",
	)
	if it.ID != "download_folio" || info.Source != "llm" || slots.GuestName != "Smith" {
		t.Fatalf("folio message was hijacked: it=%s source=%s slots=%+v", it.ID, info.Source, slots)
	}
}

func TestRouterOwnsTurnForFolioActions(t *testing.T) {
	t.Parallel()
	rc := &reqCtx{req: Request{Message: "email the folio for Smith"}}
	if !rc.routerOwnsTurn() {
		t.Fatal("folio action messages must stay on the dedicated router, not the generic planner")
	}
}

func TestHDownloadFolioDefaultDocType(t *testing.T) {
	t.Parallel()
	rc := folioFixture(t, testReservationJSON, nil)
	rc.req.Message = "download the folio for Smith"
	it, _ := intents.Get("download_folio")

	if err := hDownloadFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hDownloadFolio: %v", err)
	}
	if len(rc.resp.Cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(rc.resp.Cards))
	}
	card := rc.resp.Cards[0]
	if card.Title != "Folio" || len(card.Downloads) != 1 {
		t.Fatalf("card = %+v", card)
	}
	d := card.Downloads[0]
	if d.Service != "reservation" || d.Path != "/folio/res-1" ||
		d.Query["doc_type"] != "folio" || d.Query["reservation_type"] != "individual" {
		t.Fatalf("download = %+v", d)
	}
	if card.Link == nil || !strings.Contains(card.Link.Route, "res-1") {
		t.Fatalf("link = %+v", card.Link)
	}
}

func TestHDownloadFolioDocTypeFromMessage(t *testing.T) {
	t.Parallel()
	rc := folioFixture(t, testReservationJSON, nil)
	rc.req.Message = "download the registration form for Smith"
	it, _ := intents.Get("download_folio")

	if err := hDownloadFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hDownloadFolio: %v", err)
	}
	d := rc.resp.Cards[0].Downloads[0]
	if d.Query["doc_type"] != "registration" {
		t.Fatalf("doc_type = %q, want registration", d.Query["doc_type"])
	}
}

// First turn resolves the reservation and asks for confirmation without
// sending anything; the second turn only sends on an explicit "yes".
func TestHEmailFolioConfirmThenSend(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationJSON, &posted)
	rc.req.Message = "email the folio for Smith"
	it, _ := intents.Get("email_folio")

	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hEmailFolio (confirm step): %v", err)
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeClarify {
		t.Fatalf("expected a confirm card, got %+v", rc.resp.Cards)
	}
	if len(posted) != 0 {
		t.Fatal("email must not be sent before confirmation")
	}
	pending, ok := decodeEmailFolioPending(rc.sess.LastEntity)
	if !ok || pending.recipient != "poc@example.com" || pending.docType != "folio" {
		t.Fatalf("pending = %+v ok=%v", pending, ok)
	}

	rc.resp = &Response{}
	rc.req.Message = "yes, send it"
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("hEmailFolio (send step): %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("POST calls = %d, want 1", len(posted))
	}
	if posted[0]["email"] != "poc@example.com" || posted[0]["docType"] != "folio" || posted[0]["reservationId"] != "res-1" {
		t.Fatalf("posted body = %+v", posted[0])
	}
	if rc.sess.LastEntity != nil && rc.sess.LastEntity.Kind == emailFolioPendingKind {
		t.Fatal("pending confirmation should be cleared once sent")
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Title != "Email sent" {
		t.Fatalf("expected an email-sent card, got %+v", rc.resp.Cards)
	}
}

func TestHEmailFolioCancelDoesNotSend(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationJSON, &posted)
	rc.req.Message = "email the folio for Smith"
	it, _ := intents.Get("email_folio")

	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hEmailFolio (confirm step): %v", err)
	}

	rc.resp = &Response{}
	rc.req.Message = "no, cancel"
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("hEmailFolio (cancel step): %v", err)
	}
	if len(posted) != 0 {
		t.Fatal("email must not be sent after a cancel reply")
	}
	if rc.resp.Text != "Okay, I won't send it." {
		t.Fatalf("text = %q", rc.resp.Text)
	}
	if len(rc.resp.Cards) != 0 {
		t.Fatalf("cancel should not add a card, got %+v", rc.resp.Cards)
	}
}

// An unrelated reply after the confirm card must also cancel, never send.
func TestHEmailFolioUnclearReplyDoesNotSend(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationJSON, &posted)
	rc.req.Message = "email the folio for Smith"
	it, _ := intents.Get("email_folio")
	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hEmailFolio (confirm step): %v", err)
	}

	rc.resp = &Response{}
	rc.req.Message = "what's the weather like"
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("hEmailFolio (unclear reply): %v", err)
	}
	if len(posted) != 0 {
		t.Fatal("an unrelated reply must never trigger a send")
	}
}

func TestHEmailFolioMissingPOCEmail(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationNoPOCJSON, &posted)
	rc.req.Message = "email the folio for Smith"
	it, _ := intents.Get("email_folio")

	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hEmailFolio: %v", err)
	}
	if len(posted) != 0 {
		t.Fatal("no recipient: nothing should be sent")
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeNavigate {
		t.Fatalf("expected a navigate card pointing at the reservation, got %+v", rc.resp.Cards)
	}
	if rc.sess.LastEntity != nil && rc.sess.LastEntity.Kind == emailFolioPendingKind {
		t.Fatal("no pending confirmation should be remembered without a recipient")
	}
}
