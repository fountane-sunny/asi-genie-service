package genie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/intents"
	genillm "asi-genie-poc/llm"
	"asi-genie-poc/toolregistry/generated"
)

const (
	testReservationJSON      = `{"id":"res-1","reservationNumber":"4471","guestName":"John Smith","pocEmail":"poc@example.com","type":"INDIVIDUAL"}`
	testReservationNoPOCJSON = `{"id":"res-1","reservationNumber":"4471","guestName":"John Smith","type":"INDIVIDUAL"}`
)

func folioFixture(t *testing.T, reservationJSON string, posted *[]map[string]any) *reqCtx {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/reservations/search":
			_, _ = w.Write([]byte(`{"data":[` + reservationJSON + `]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/reservations/res-1":
			_, _ = w.Write([]byte(`{"data":` + reservationJSON + `}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/folio/email":
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
		"I want to print folio also":                 true,
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

func TestApplyFolioIntentOverridesLegacyGuide(t *testing.T) {
	t.Parallel()
	legacy, ok := intents.Get("folio_document")
	if !ok {
		t.Fatal("folio_document missing from catalogue")
	}
	cases := []struct {
		msg  string
		want string
	}{
		{"I want to print folio also", "download_folio"},
		{"print the folio for 305", "download_folio"},
		{"download the folio for Smith", "download_folio"},
		{"email the folio for Smith", "email_folio"},
		{"send all the folios for Smith", "email_folio"},
	}
	for _, tc := range cases {
		it, info, _ := (&Engine{}).applyFolioIntent(
			legacy,
			IntentInfo{ID: legacy.ID, ScenarioID: legacy.ScenarioID, ResponseType: legacy.ResponseType, Source: "llm"},
			Slots{},
			tc.msg,
		)
		if it.ID != tc.want || info.ID != tc.want || info.Source != "rule" {
			t.Errorf("applyFolioIntent(%q) = %s source=%s, want %s rule", tc.msg, it.ID, info.Source, tc.want)
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

func TestYesSendThemUsesConfirmInHistory(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationJSON, &posted)
	rc.req.Message = "yes, send them"
	rc.req.History = []genillm.Message{{
		Role:    "assistant",
		Content: "Shown: Email the registration form, folio, detailed folio, and other charges folio for John Smith to poc@example.com — send them?",
	}}
	it, _ := intents.Get("email_folio")

	if !rc.routerOwnsTurn() {
		t.Fatal("a folio email confirmation in history must stay off the planner")
	}
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("hEmailFolio: %v", err)
	}
	got := make([]string, 0, len(posted))
	for _, body := range posted {
		got = append(got, fmt.Sprint(body["docType"]))
	}
	want := []string{"registration", "folio", "detailed", "other-charges"}
	if strings.Join(got, ",") != strings.Join(want, ",") || posted[0]["email"] != "poc@example.com" {
		t.Fatalf("posted = %+v", posted)
	}
}

func TestRouterOwnsEmailFolioConfirmation(t *testing.T) {
	t.Parallel()
	pending := encodeEmailFolioPending(pendingEmailFolio{})
	rc := &reqCtx{
		req:  Request{Message: "yes, send it"},
		sess: &session{LastEntity: &pending},
	}
	if !rc.routerOwnsTurn() {
		t.Fatal("a pending folio email confirmation must stay on the dedicated router")
	}
}

func TestBarePrintFollowUpDownloadsStandardFolio(t *testing.T) {
	t.Parallel()
	rc := folioFixture(t, testReservationJSON, nil)
	rc.req.Message = "i want to print"
	rc.sess.LastIntent = "download_folio"
	rc.sess.LastEntity = &Entity{Kind: "reservation", ID: "4471", Label: "John Smith"}
	it, _ := intents.Get("download_folio")

	if !rc.isBareFolioPrintFollowUp() {
		t.Fatal("bare print after a folio card must stay on the folio flow")
	}
	if err := hDownloadFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("hDownloadFolio: %v", err)
	}
	if len(rc.resp.Cards) != 1 || len(rc.resp.Cards[0].Downloads) != 1 {
		t.Fatalf("expected one default folio download, got %+v", rc.resp.Cards)
	}
	download := rc.resp.Cards[0].Downloads[0]
	if download.Label != "Download PDF" || download.Query["doc_type"] != "folio" {
		t.Fatalf("download = %+v, want standard folio PDF", download)
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
	want := []string{"registration", "folio", "detailed", "other-charges"}
	if card.Title != "Folio" || len(card.Downloads) != len(want) {
		t.Fatalf("card = %+v", card)
	}
	for i, docType := range want {
		d := card.Downloads[i]
		if d.Label != folioDocLabel(docType) || d.Service != "reservation" || d.Path != "/folio/res-1" ||
			d.Query["doc_type"] != docType || d.Query["reservation_type"] != "individual" || d.Query["propertyId"] != "42" {
			t.Fatalf("download %d = %+v", i, d)
		}
	}
	if card.Link == nil || !strings.Contains(card.Link.Route, "res-1") {
		t.Fatalf("link = %+v", card.Link)
	}
}

func TestHDownloadFolioGroupDocuments(t *testing.T) {
	t.Parallel()
	rc := folioFixture(t, `{"id":"res-1","reservationNumber":"4471","guestName":"John Smith","type":"GROUP"}`, nil)
	rc.req.Message = "download folio reservation 4471"
	it, _ := intents.Get("download_folio")

	if err := hDownloadFolio(rc, it, Slots{Confirmation: "4471"}); err != nil {
		t.Fatalf("hDownloadFolio: %v", err)
	}
	got := make([]string, 0, len(rc.resp.Cards[0].Downloads))
	for _, d := range rc.resp.Cards[0].Downloads {
		if d.Query["reservation_type"] != "group" {
			t.Fatalf("reservation_type = %q", d.Query["reservation_type"])
		}
		got = append(got, d.Query["doc_type"])
	}
	want := []string{"folio", "detailed", "other-charges", "group-status-report"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("doc types = %v, want %v", got, want)
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
	if len(rc.resp.Cards[0].Downloads) != 1 {
		t.Fatalf("downloads = %d, want the named document only", len(rc.resp.Cards[0].Downloads))
	}
	d := rc.resp.Cards[0].Downloads[0]
	if d.Query["doc_type"] != "registration" || d.Label != "Download PDF" {
		t.Fatalf("download = %+v", d)
	}
}

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
	sent, ok := decodeEmailFolioSent(rc.sess.LastEntity)
	if !ok || sent.docType != "folio" {
		t.Fatalf("sent email context = %+v ok=%v", sent, ok)
	}
}

func TestHEmailFolioAllDocuments(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationJSON, &posted)
	rc.req.Message = "send all the folios for Smith"
	it, _ := intents.Get("email_folio")

	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hEmailFolio (confirm step): %v", err)
	}
	if len(posted) != 0 {
		t.Fatal("all documents must wait for confirmation")
	}
	if !strings.Contains(rc.resp.Cards[0].Title, "registration form") || !strings.Contains(rc.resp.Cards[0].Title, "other charges folio") {
		t.Fatalf("confirm message = %q", rc.resp.Cards[0].Title)
	}

	rc.resp = &Response{}
	rc.req.Message = "yes, send them"
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("hEmailFolio (send step): %v", err)
	}
	got := make([]string, 0, len(posted))
	for _, body := range posted {
		got = append(got, fmt.Sprint(body["docType"]))
	}
	want := []string{"registration", "folio", "detailed", "other-charges"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sent doc types = %v, want %v", got, want)
	}

	rc.resp = &Response{}
	rc.req.Message = "all folios sent?"
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("hEmailFolio (status step): %v", err)
	}
	if !strings.HasPrefix(rc.resp.Text, "Yes.") {
		t.Fatalf("status text = %q", rc.resp.Text)
	}
	if !strings.Contains(rc.resp.Text, "reservation 4471") || !strings.Contains(rc.resp.Text, "sent at") {
		t.Fatalf("status text = %q, want reservation number and send time", rc.resp.Text)
	}
}

func TestHEmailFolioSentStatusAnswersForSelectedDocument(t *testing.T) {
	t.Parallel()
	rc := folioFixture(t, testReservationJSON, nil)
	sent := encodeEmailFolioPending(pendingEmailFolio{
		reservationID: "res-1", docType: "folio", reservationType: "individual",
		recipient: "poc@example.com", guestName: "John Smith",
	})
	sent.Kind = emailFolioSentKind
	rc.sess.LastEntity = &sent
	rc.sess.LastIntent = "email_folio"
	rc.req.Message = "all folios sent?"
	it, _ := intents.Get("email_folio")

	if !rc.routerOwnsTurn() {
		t.Fatal("email-send status questions must stay on the dedicated router")
	}
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("hEmailFolio: %v", err)
	}
	if !strings.Contains(rc.resp.Text, "only the folio") || !strings.Contains(rc.resp.Text, "were not sent") {
		t.Fatalf("status text = %q", rc.resp.Text)
	}
}

func TestHEmailFolioAlreadySentSkipsAnotherSend(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationJSON, &posted)
	it, _ := intents.Get("email_folio")

	rc.req.Message = "email the folio for Smith"
	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	rc.resp = &Response{}
	rc.req.Message = "yes, send it"
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("POST calls = %d, want 1", len(posted))
	}

	rc.resp = &Response{}
	rc.req.Message = "email the folio for Smith"
	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("repeat must not email again, POST calls = %d", len(posted))
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Title != "Folio already sent" {
		t.Fatalf("cards = %+v", rc.resp.Cards)
	}
	if !strings.Contains(rc.resp.Text, "already sent") || !strings.Contains(rc.resp.Text, "reservation 4471") || !strings.Contains(rc.resp.Text, "sent at") {
		t.Fatalf("text = %q", rc.resp.Text)
	}

	rc.resp = &Response{}
	rc.req.Message = "yes, send it"
	if err := hEmailFolio(rc, it, Slots{}); err != nil {
		t.Fatalf("second yes: %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("second yes must not email again, POST calls = %d", len(posted))
	}
	if !strings.Contains(rc.resp.Text, "already sent") {
		t.Fatalf("text = %q", rc.resp.Text)
	}
}

func TestHEmailFolioAlreadySentIncludesSender(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationJSON, &posted)
	sentAt := time.Date(2026, 10, 21, 8, 56, 0, 0, time.UTC)
	sent := encodeEmailFolioPending(pendingEmailFolio{
		reservationID: "res-1", docType: "folio", reservationType: "individual",
		recipient: "poc@example.com", guestName: "John Smith",
		reservationNumber: "4471", sentAt: sentAt, sentBy: "Sunny Sahsi",
	})
	sent.Kind = emailFolioSentKind
	rc.sess.LastEntity = &sent
	rc.req.Message = "send the folio for Smith"
	it, _ := intents.Get("email_folio")

	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hEmailFolio: %v", err)
	}
	if len(posted) != 0 {
		t.Fatal("an already-sent folio must not be emailed again")
	}
	for _, want := range []string{"already sent", "reservation 4471", "Oct 21, 2026 8:56 AM UTC", "Sunny Sahsi"} {
		if !strings.Contains(rc.resp.Text, want) {
			t.Fatalf("text = %q, missing %q", rc.resp.Text, want)
		}
	}
}

func TestHEmailFolioSendAgainStillConfirms(t *testing.T) {
	t.Parallel()
	var posted []map[string]any
	rc := folioFixture(t, testReservationJSON, &posted)
	sent := encodeEmailFolioPending(pendingEmailFolio{
		reservationID: "res-1", docType: "folio", reservationType: "individual",
		recipient: "poc@example.com", guestName: "John Smith", reservationNumber: "4471",
	})
	sent.Kind = emailFolioSentKind
	rc.sess.LastEntity = &sent
	rc.req.Message = "email the folio again"
	it, _ := intents.Get("email_folio")

	if err := hEmailFolio(rc, it, Slots{GuestName: "Smith"}); err != nil {
		t.Fatalf("hEmailFolio: %v", err)
	}
	if len(posted) != 0 {
		t.Fatal("send again must wait for confirmation")
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeClarify {
		t.Fatalf("cards = %+v", rc.resp.Cards)
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

// A confirmation search must use searchKey. The service ignores swagger's "key", and an empty searchKey browses current stays.
func TestResolveReservationUsesSearchKeyAndExactNumber(t *testing.T) {
	t.Parallel()
	const confirmation = "F20261015075416"
	var search url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/reservations/search":
			search = r.URL.Query()
			_, _ = w.Write([]byte(`{"data":[
				{"entryType":"REPORT","reportName":"Sales"},
				{"entryType":"RESERVATION","reservationId":"res-other","reservationNumber":"F20261015075418","pocName":"Sunny Sahsi"},
				{"entryType":"RESERVATION","reservationId":"res-1","reservationNumber":"F20261015075416","pocName":"Sunny Sahsi","checkInDateTime":"2026-10-15T15:00:00Z","checkOutDateTime":"2026-10-16T11:00:00Z"}
			]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/reservations/res-1":
			_, _ = w.Write([]byte(`{"data":` + testReservationJSON + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	rc := folioFixture(t, testReservationJSON, nil)
	for _, s := range []generated.Service{generated.AuthService, generated.ConfigService, generated.ReservationService} {
		rc.e.HTTP.BaseURLs[s.Name] = srv.URL
	}
	it, _ := intents.Get("download_folio")
	rc.intent = it
	rc.req.Message = "download pdf folio for " + confirmation

	if err := hDownloadFolio(rc, it, Slots{Confirmation: confirmation}); err != nil {
		t.Fatalf("hDownloadFolio: %v", err)
	}
	if search.Get("searchKey") != confirmation || search.Get("key") != "" || search.Get("entryType") != "RESERVATION" {
		t.Fatalf("search query = %s", search.Encode())
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeData || len(rc.resp.Cards[0].Downloads) != 1 {
		t.Fatalf("expected a download for the exact confirmation, got %+v", rc.resp.Cards)
	}
}

func TestResolveReservationDisambiguationSkipsReportsAndEmptyDates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/reservations/search" {
			_, _ = w.Write([]byte(`{"data":[
				{"entryType":"REPORT"},
				{"entryType":"RESERVATION","reservationId":"a","reservationNumber":"F1","pocName":"Sunny Sahsi"},
				{"entryType":"RESERVATION","reservationId":"b","reservationNumber":"F2","pocName":"Riyaz Ramesh","checkInDateTime":"2026-10-15T00:00:00Z","checkOutDateTime":"2026-10-16T00:00:00Z"}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	rc := folioFixture(t, testReservationJSON, nil)
	for _, s := range []generated.Service{generated.AuthService, generated.ConfigService, generated.ReservationService} {
		rc.e.HTTP.BaseURLs[s.Name] = srv.URL
	}
	it, _ := intents.Get("download_folio")
	rc.intent = it
	rc.setIntent(IntentInfo{ID: it.ID, Handler: "dedicated"})

	if err := hDownloadFolio(rc, it, Slots{GuestName: "Sahsi"}); err != nil {
		t.Fatalf("hDownloadFolio: %v", err)
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeDisambiguation {
		t.Fatalf("expected disambiguation, got %+v", rc.resp.Cards)
	}
	opts := rc.resp.Cards[0].Options
	if len(opts) != 2 {
		t.Fatalf("options = %+v", opts)
	}
	if opts[0].Label != "Sunny Sahsi #F1" || opts[1].Label != "Riyaz Ramesh #F2 2026-10-15→2026-10-16" {
		t.Fatalf("labels = %q, %q", opts[0].Label, opts[1].Label)
	}
}
