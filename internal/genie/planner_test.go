package genie

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"asi-genie-poc/internal/cards"
	"asi-genie-poc/internal/store"
	genillm "asi-genie-poc/llm"
	"asi-genie-poc/toolregistry/generated"
)

type scriptedLLM struct {
	mu      sync.Mutex
	replies []genillm.Response
	errs    []error
	calls   []scriptedCall
}

type scriptedCall struct {
	system string
	msgs   []genillm.Message
	tools  []genillm.Tool
}

func (s *scriptedLLM) SendMessage(_ context.Context, system string, msgs []genillm.Message, tools []genillm.Tool) (genillm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := len(s.calls)
	s.calls = append(s.calls, scriptedCall{system, msgs, tools})
	if i < len(s.errs) && s.errs[i] != nil {
		return genillm.Response{}, s.errs[i]
	}
	if i < len(s.replies) {
		return s.replies[i], nil
	}
	return genillm.Response{}, errors.New("no scripted reply")
}

type logOnlyStore struct{ store.Store }

func (logOnlyStore) LogToolExecution(context.Context, store.ToolExecution) error { return nil }

func toolCall(t *testing.T, name string, args any) genillm.Response {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return genillm.Response{ToolCalls: []genillm.ToolCall{{ID: "1", Name: name, Arguments: b}}}
}

func planCall(t *testing.T, p readPlan) genillm.Response { return toolCall(t, planTool, p) }

func plannerFixture(t *testing.T, llm *scriptedLLM) (*reqCtx, *[]*http.Request) {
	t.Helper()
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/room-space-statuses":
			_, _ = w.Write([]byte(`{"data":[{"roomNumber":"101","status":"Vacant"}],"total":1}`))
		case "/v1/properties/42/users":
			_, _ = w.Write([]byte(`{"data":[{"firstName":"Sunny","email":"sunny@example.com"}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	e := New(llm, logOnlyStore{})
	for _, s := range []generated.Service{generated.AuthService, generated.ConfigService, generated.ReservationService} {
		e.HTTP.BaseURLs[s.Name] = srv.URL
	}
	rc := &reqCtx{e: e, ctx: context.Background(), token: "tok", pid: 42, bizDate: "2026-10-20",
		req: Request{Message: "list all the roroms", PropertyID: 42}, resp: &Response{}}
	return rc, &seen
}

func TestPlannerReservationSearchRenamesKeyToSearchKey(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{
			{Tool: "reservation_get_reservations_search", Query: map[string]string{"key": "F20261015075416"}},
		}}),
		{Text: "Found it."},
	}}
	rc, seen := plannerFixture(t, llm)
	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the turn")
	}
	if len(*seen) != 1 || (*seen)[0].URL.Path != "/api/v1/reservations/search" {
		t.Fatalf("calls = %d", len(*seen))
	}
	q := (*seen)[0].URL.Query()
	if q.Get("searchKey") != "F20261015075416" || q.Get("key") != "" {
		t.Fatalf("query = %s", q.Encode())
	}
}

func TestPlannerIndexToolsAreAllowedReads(t *testing.T) {
	e := New(nil, logOnlyStore{})
	for _, pt := range plannerIndex {
		if _, ok := e.Tools[pt.Name]; !ok {
			t.Errorf("planner tool %s is not an allowed generated read", pt.Name)
		}
	}
}

func TestPlannerRunsReadsAndPresentsTable(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{
			{Tool: "config_get_room_space_statuses", Query: map[string]string{"pageSize": "100", "propertyId": "999"}},
		}}),
		toolCall(t, presentTool, presentedAnswer{
			Summary: "You have 1 room.",
			Table: &presentedTable{
				Columns: []cards.Column{{Key: "room", Label: "Room"}, {Key: "status", Label: "Status"}},
				Rows:    []map[string]any{{"room": "101", "status": "Vacant"}},
			},
		}),
	}}
	rc, seen := plannerFixture(t, llm)

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the turn")
	}
	if len(*seen) != 1 {
		t.Fatalf("API calls = %d, want 1", len(*seen))
	}
	if got := (*seen)[0].URL.Query().Get("propertyId"); got != "42" {
		t.Errorf("propertyId = %q, want the active property 42 (model asked for 999)", got)
	}
	if rc.resp.Text != "You have 1 room." {
		t.Errorf("text = %q", rc.resp.Text)
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeTable {
		t.Fatalf("cards = %+v, want one table", rc.resp.Cards)
	}
	if got := rc.resp.Cards[0].Table.Rows[0]["status"]; got != "Vacant" {
		t.Errorf("table status = %q", got)
	}
	if len(llm.calls) != 2 || len(llm.calls[1].tools) != 1 || llm.calls[1].tools[0].Name != presentTool {
		t.Fatal("answer call must offer only present_answer")
	}
	if !strings.Contains(llm.calls[1].msgs[0].Content, `"roomNumber":"101"`) {
		t.Error("answer call did not receive the read result")
	}
}

func TestPlannerTextAnswerStillWorks(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{{Tool: "config_get_room_space_statuses"}}}),
		{Text: "Room 101 is vacant."},
	}}
	rc, _ := plannerFixture(t, llm)

	if !rc.e.planAndAnswer(rc) || rc.resp.Text != "Room 101 is vacant." || len(rc.resp.Cards) != 0 {
		t.Fatalf("text=%q cards=%d", rc.resp.Text, len(rc.resp.Cards))
	}
}

func TestPlannerRefusalMakesNoAPICalls(t *testing.T) {
	const written = "I can't take payments from chat; use the payment terminal flow. I can show the folio balance if you like."
	llm := &scriptedLLM{replies: []genillm.Response{planCall(t, readPlan{Action: planActionRefuse, Message: written})}}
	rc, seen := plannerFixture(t, llm)
	rc.req.Message = "can you charge the guest's card"

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the refusal")
	}
	if len(*seen) != 0 || len(rc.resp.ToolCalls) != 0 {
		t.Errorf("refusal made API calls: %d", len(*seen))
	}
	if rc.resp.Text != written {
		t.Errorf("text = %q", rc.resp.Text)
	}
}

func TestPlannerPromptKeepsNonHotelRefusalsShort(t *testing.T) {
	rc := &reqCtx{e: New(nil, logOnlyStore{}), req: Request{Message: "what is python?"}, resp: &Response{}}
	sys := rc.plannerSystem()
	if !strings.Contains(sys, "Do not define it") || !strings.Contains(sys, "still action=refuse") {
		t.Fatal("a non-hotel topic and its follow-up must stay a one-sentence refusal")
	}
	if !strings.Contains(sys, "in danger") {
		t.Fatal("someone in danger must still be told the emergency number")
	}
}

func TestPlannerRefusalWithoutMessageFallsBackToConstant(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{planCall(t, readPlan{Action: planActionRefuse})}}
	rc, _ := plannerFixture(t, llm)
	rc.req.Message = "what is the capital of France"

	if !rc.e.planAndAnswer(rc) || rc.resp.Text != scopeRefusal {
		t.Errorf("text = %q, want the fallback refusal", rc.resp.Text)
	}
}

func TestPlannerCreateEntityRefusalUsesVerifiedScreenNotModelText(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRefuse, Entity: "room_type", Message: "ignored: add one under Settings > Rooms"}),
	}}
	rc, seen := plannerFixture(t, llm)
	rc.req.Message = "can you create new rom tpye?"

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the refusal")
	}
	if len(*seen) != 0 {
		t.Errorf("refusal made API calls: %d", len(*seen))
	}
	const want = "I can't create a room type from chat - use Open room types to add one. I can show you the current room types if that helps."
	if rc.resp.Text != want {
		t.Errorf("text = %q, want %q", rc.resp.Text, want)
	}
	if len(rc.resp.Cards) != 1 || rc.resp.Cards[0].Type != cards.TypeNavigate {
		t.Fatalf("cards = %+v, want one navigate card", rc.resp.Cards)
	}
	if got, want := rc.resp.Cards[0].Link.Route, "/property/42?tab=room-types"; got != want {
		t.Errorf("route = %q, want %q", got, want)
	}
}

func TestPlannerCreateEntityRefusalWithoutReadOffer(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRefuse, Entity: "amenity"}),
	}}
	rc, _ := plannerFixture(t, llm)
	rc.req.Message = "add a new amenity"

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the refusal")
	}
	const want = "I can't create an amenity from chat - use Open amenities to add one."
	if rc.resp.Text != want {
		t.Errorf("text = %q, want %q", rc.resp.Text, want)
	}
}

func TestPlannerUpdateEntityRefusalUsesListScreen(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRefuse, Entity: "email_template", WriteKind: writeKindUpdate}),
	}}
	rc, _ := plannerFixture(t, llm)
	rc.req.Message = "how can i update the email tempplates?"

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the refusal")
	}
	const want = "I can't update an email template from chat - use Open email templates to edit one."
	if rc.resp.Text != want {
		t.Errorf("text = %q, want %q", rc.resp.Text, want)
	}
	if got, want := rc.resp.Cards[0].Link.Route, "/property/42/email-templates"; got != want {
		t.Errorf("route = %q, want %q (the list, not /new)", got, want)
	}
}

func TestPlannerUpdateEntityRefusalReusesTabScreen(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRefuse, Entity: "room_type", WriteKind: writeKindUpdate}),
	}}
	rc, _ := plannerFixture(t, llm)
	rc.req.Message = "update the deluxe room type"

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the refusal")
	}
	const want = "I can't update a room type from chat - use Open room types to edit one. I can show you the current room types if that helps."
	if rc.resp.Text != want {
		t.Errorf("text = %q, want %q", rc.resp.Text, want)
	}
	if got, want := rc.resp.Cards[0].Link.Route, "/property/42?tab=room-types"; got != want {
		t.Errorf("route = %q, want %q", got, want)
	}
}

func TestPlannerUpdateReservationFallsBackToModelMessage(t *testing.T) {
	const written = "I can't update a reservation from chat; search for it, then open it to edit."
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRefuse, Entity: "reservation", WriteKind: writeKindUpdate, Message: written}),
	}}
	rc, _ := plannerFixture(t, llm)
	rc.req.Message = "can you update a reservation"

	if !rc.e.planAndAnswer(rc) || rc.resp.Text != written {
		t.Errorf("text = %q, want the model's message %q", rc.resp.Text, written)
	}
	if len(rc.resp.Cards) != 0 {
		t.Errorf("cards = %+v, want none (no verified single-record screen)", rc.resp.Cards)
	}
}

func TestCreateGuideEntityEnumMatchesTable(t *testing.T) {
	enum := createGuideEntityEnum()
	if len(enum) != len(createGuideTable) {
		t.Fatalf("enum has %d entries, want %d", len(enum), len(createGuideTable))
	}
	for _, g := range createGuideTable {
		if _, ok := createGuideByID[g.id]; !ok {
			t.Errorf("createGuideByID missing id %q", g.id)
		}
	}
}

func TestPlannerSkipsToolsThatAreNotAllowedReads(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{
			{Tool: "reservation_post_payments"},
			{Tool: "config_get_properties_id_users", Query: map[string]string{"searchKey": "sunny"}},
		}}),
		{Text: "Sunny is a user on this property."},
	}}
	rc, seen := plannerFixture(t, llm)

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the turn")
	}
	if len(*seen) != 1 || (*seen)[0].URL.Path != "/v1/properties/42/users" {
		t.Fatalf("want only the users read, got %d calls", len(*seen))
	}
	if !strings.Contains(llm.calls[1].msgs[0].Content, `"status":"skipped"`) {
		t.Error("answer call should see the skipped tool")
	}
}

func TestPlannerKeepsEarlierFileWhenAskIsUnavailable(t *testing.T) {
	rc, _ := plannerFixture(t, &scriptedLLM{})
	sys := rc.plannerSystem()
	if !strings.Contains(sys, "Do not say that earlier message was wrong") {
		t.Error("planner prompt should keep an earlier download when the new ask is unavailable")
	}
	if !strings.Contains(answerPrompt(), "Do not say an earlier download or export was wrong") {
		t.Error("answer prompt should keep an earlier download when the new ask is unavailable")
	}
}

func TestPlannerSystemOffersEveryAllowedRead(t *testing.T) {
	rc, _ := plannerFixture(t, &scriptedLLM{})
	rc.req.Message = "daily collection by payment type for today"
	sys := rc.plannerSystem()
	for name := range rc.e.Tools {
		if !strings.Contains(sys, name) {
			t.Errorf("allowed read %s is hidden from the planner", name)
		}
	}
	if strings.Contains(sys, "_export") || strings.Contains(sys, "reservation_post_") {
		t.Error("planner prompt lists an export or write tool")
	}
	if !strings.Contains(sys, "- reservation_get_api_v1_reports_daily_collection_by_payment_type: ") ||
		!strings.Contains(sys, "[query: date*, paymentMethods") {
		t.Error("the report should be listed with its required date param")
	}
	t.Logf("planner system prompt: %d bytes for %d allowed reads", len(sys), len(rc.e.Tools))
}

func TestPlannerRunsReportOutsideCoreWithBusinessDate(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{
			{Tool: "reservation_get_api_v1_reports_advance_deposit_ledger"},
			{Tool: "reservation_get_api_v1_reports_daily_collection_by_payment_type"},
			{Tool: "reservation_get_api_v1_reports_housekeeping_task_summary", Query: map[string]string{"startDate": "2026-10-01"}},
		}}),
		{Text: "No deposits, collections or tasks."},
	}}
	rc, seen := plannerFixture(t, llm)
	if _, ok := plannerByName["reservation_get_api_v1_reports_advance_deposit_ledger"]; ok {
		t.Fatal("test needs a report outside the core index")
	}

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the turn")
	}
	got := map[string]string{}
	for _, r := range *seen {
		got[r.URL.Path] = r.URL.RawQuery
	}
	want := map[string]string{
		"/api/v1/reports/advance-deposit-ledger": "endDate=2026-10-20&propertyId=42&startDate=2026-10-20",
		// An unset comma-separated filter is sent as every value; a missing param matches nothing.
		"/api/v1/reports/daily-collection-by-payment-type": "date=2026-10-20&paymentMethods=creditCard%2CdebitCard%2Ccash%2Ccheck%2CbadDebts%2CbusinessSource&propertyId=42&status=IN_HOUSE%2CCHECKED_OUT%2CCONFIRMED%2CUNCONFIRMED%2CTRANSFER_OUT%2CCANCELLED%2CNO_SHOW",
		"/api/v1/reports/housekeeping-task-summary":        "endDate=2026-10-01&propertyId=42&startDate=2026-10-01",
	}
	for path, q := range want {
		if got[path] != q {
			t.Errorf("%s query = %q, want %q", path, got[path], q)
		}
	}
	if label := ToolLabel("reservation_get_api_v1_reports_advance_deposit_ledger"); label != "Checking advance deposit ledger" {
		t.Errorf("label = %q", label)
	}
}

func TestPlannerReportsMissingRequiredParamWithoutCalling(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{
			{Tool: "reservation_get_email_templates_applicable"},
		}}),
		planCall(t, readPlan{Action: planActionRead}),
		{Text: "Which reservation?"},
	}}
	rc, seen := plannerFixture(t, llm)

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the turn")
	}
	if len(*seen) != 0 {
		t.Fatalf("want no API call without reservationId, got %d", len(*seen))
	}
	if len(llm.calls) != 3 {
		t.Fatalf("LLM calls = %d, want plan, retry plan, answer", len(llm.calls))
	}
	retry := llm.calls[1].msgs[len(llm.calls[1].msgs)-1].Content
	if !strings.Contains(retry, "required query params not set: reservationId") {
		t.Errorf("retry round should see the missing param, got %s", retry)
	}
}

func TestFillEnumDefaultsSendsAllUnlessModelNarrowed(t *testing.T) {
	tool := generated.ToolMeta{Query: []generated.QueryParam{
		{Name: "paymentMethods", AllValues: []string{"creditCard", "debitCard", "cash"}},
		{Name: "status", AllValues: []string{"IN_HOUSE", "CHECKED_OUT"}},
		{Name: "date"},
	}}

	unset := url.Values{}
	fillEnumDefaults(tool, unset)
	if got := unset.Get("paymentMethods"); got != "creditCard,debitCard,cash" {
		t.Errorf("unset paymentMethods = %q, want every value", got)
	}
	if got := unset.Get("status"); got != "IN_HOUSE,CHECKED_OUT" {
		t.Errorf("unset status = %q, want every value", got)
	}
	if unset.Get("date") != "" {
		t.Error("a param without AllValues must not be touched")
	}

	narrowed := url.Values{"paymentMethods": {"cash"}}
	fillEnumDefaults(tool, narrowed)
	if got := narrowed.Get("paymentMethods"); got != "cash" {
		t.Errorf("model-set paymentMethods = %q, want unchanged \"cash\"", got)
	}
	if got := narrowed.Get("status"); got != "IN_HOUSE,CHECKED_OUT" {
		t.Errorf("status still unset, want defaulted to every value, got %q", got)
	}
}

func TestPlannerOnlyCashStaysNarrowed(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{
			{Tool: "reservation_get_api_v1_reports_daily_collection_by_payment_type", Query: map[string]string{"paymentMethods": "cash"}},
		}}),
		{Text: "Cash collections for today."},
	}}
	rc, seen := plannerFixture(t, llm)

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the turn")
	}
	if len(*seen) != 1 {
		t.Fatalf("want 1 call, got %d", len(*seen))
	}
	q := (*seen)[0].URL.Query()
	if q.Get("paymentMethods") != "cash" {
		t.Errorf(`"only cash" must stay narrowed, got paymentMethods=%q`, q.Get("paymentMethods"))
	}
	if q.Get("status") == "" {
		t.Error("status has no AllValues set by the user, so it should still default to every value")
	}
}

func TestClientErrorDetailOnlyForClientErrors(t *testing.T) {
	if d := clientErrorDetail(errors.New(`GET /x: HTTP 400: {"message":"date is required"}`)); !strings.Contains(d, "date is required") {
		t.Errorf("400 detail = %q", d)
	}
	if d := clientErrorDetail(errors.New(`GET /x: HTTP 500: panic at db.go:12`)); d != "" {
		t.Errorf("500 must not leak server internals, got %q", d)
	}
	if d := clientErrorDetail(errors.New(`GET /x: HTTP 400: card 4111111111111111 invalid`)); strings.Contains(d, "4111111111111111") {
		t.Errorf("card number not masked: %q", d)
	}
}

func TestToolLabel(t *testing.T) {
	cases := map[string]string{
		"config_get_room_types":                      "Checking room types",
		"reservation_get_api_v1_reports_daily_shift": "Checking daily shift report",
		"not_a_tool": defaultToolLabel,
	}
	for name, want := range cases {
		if got := ToolLabel(name); got != want {
			t.Errorf("ToolLabel(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestPlannerFollowUpRoundUsesFirstResults(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, FollowUp: true, Reads: []plannedRead{
			{Tool: "config_get_properties_id_users", Query: map[string]string{"searchKey": "sunny"}},
		}}),
		planCall(t, readPlan{Action: planActionRead}),
		{Text: "Sunny — sunny@example.com"},
	}}
	rc, _ := plannerFixture(t, llm)

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the turn")
	}
	if len(llm.calls) != 3 {
		t.Fatalf("LLM calls = %d, want plan, follow-up plan, answer", len(llm.calls))
	}
	last := llm.calls[1].msgs[len(llm.calls[1].msgs)-1].Content
	if !strings.Contains(last, "sunny@example.com") {
		t.Error("follow-up plan did not receive first-round results")
	}
}

func TestPlannerLLMFailureFallsBack(t *testing.T) {
	llm := &scriptedLLM{errs: []error{errors.New("overloaded")}}
	rc, seen := plannerFixture(t, llm)

	if rc.e.planAndAnswer(rc) {
		t.Fatal("planner should hand the turn back to the intent router")
	}
	if !rc.llmFailed || len(*seen) != 0 || rc.resp.Text != "" {
		t.Errorf("llmFailed=%v calls=%d text=%q", rc.llmFailed, len(*seen), rc.resp.Text)
	}
}

func TestPlannerAnswerFailureFallsBack(t *testing.T) {
	llm := &scriptedLLM{
		replies: []genillm.Response{planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{{Tool: "config_get_room_space_statuses"}}})},
		errs:    []error{nil, errors.New("timeout")},
	}
	rc, _ := plannerFixture(t, llm)

	if rc.e.planAndAnswer(rc) {
		t.Fatal("answer failure should fall back")
	}
	if len(rc.resp.ToolCalls) != 0 {
		t.Error("fallback should start with no recorded tool calls")
	}
}

func TestRouterOwnsExportTurns(t *testing.T) {
	for _, msg := range []string{"download the arrivals report as excel", "export guest list to csv"} {
		rc := &reqCtx{req: Request{Message: msg}, resp: &Response{}}
		if !rc.routerOwnsTurn() {
			t.Errorf("%q should stay on the intent router", msg)
		}
	}
}

func TestSubjectFromQuestion(t *testing.T) {
	cases := map[string]string{
		"show Daily Collection by Payment Type Report in table": "Daily Collection by Payment Type",
		"please show me the cash only":                          "Cash only",
		"Who arrives today?":                                    "Who arrives today",
		"ASI Genie":                                             "ASI Genie",
	}
	for in, want := range cases {
		if got := subjectFromQuestion(in); got != want {
			t.Errorf("subjectFromQuestion(%q) = %q, want %q", in, got, want)
		}
	}
	if got := subjectFromQuestion("show daily collection"); got != "Daily collection" {
		t.Errorf("lowercase question should be capitalized, got %q", got)
	}
}

func TestPresentReplacesGenericTitle(t *testing.T) {
	rc := &reqCtx{req: Request{Message: "show Daily Collection by Payment Type Report in table"}, resp: &Response{}}
	ok := rc.present(presentedAnswer{
		Summary: "Two payments.",
		Stats:   &presentedFacts{Title: "ASI Genie", Rows: []presentedStat{{Label: "Cash collected", Value: "12.00"}}},
		Table: &presentedTable{
			Title:   "Table",
			Columns: []cards.Column{{Key: "method", Label: "Payment Method"}},
			Rows:    []map[string]any{{"method": "Cash"}},
		},
	})
	if !ok {
		t.Fatal("present returned false")
	}
	if len(rc.resp.Cards) != 2 {
		t.Fatalf("cards = %d, want stats and table", len(rc.resp.Cards))
	}
	for _, c := range rc.resp.Cards {
		if c.Title != "Daily Collection by Payment Type" {
			t.Errorf("%s title = %q", c.Type, c.Title)
		}
	}
}

func TestDoItSeesThePreviousAssistantOffer(t *testing.T) {
	offer := "I can't set a reminder. I can look up the Patel reservation. Should I search for it now?"
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRead, Reads: []plannedRead{
			{Tool: "reservation_get_guests", Query: map[string]string{"searchKey": "Patel"}},
		}}),
		{Text: "No Patel reservation on file."},
	}}
	rc, _ := plannerFixture(t, llm)
	rc.req.Message = "Do it"
	rc.req.History = []genillm.Message{
		{Role: genillm.RoleUser, Content: "Remind me at 3 PM to call the Patel family about their ETA"},
		{Role: genillm.RoleAssistant, Content: offer},
	}

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("planner did not handle the turn")
	}
	if len(llm.calls) < 2 {
		t.Fatalf("LLM calls = %d, want plan and answer", len(llm.calls))
	}
	for i, call := range llm.calls[:2] {
		var sawOffer bool
		for _, m := range call.msgs {
			if m.Role == genillm.RoleAssistant && strings.Contains(m.Content, "Patel reservation") {
				sawOffer = true
			}
		}
		if !sawOffer {
			t.Errorf("call %d did not include the previous assistant offer", i)
		}
	}
	last := llm.calls[1].msgs[len(llm.calls[1].msgs)-1]
	if last.Role != genillm.RoleUser || !strings.Contains(last.Content, "Question: Do it") {
		t.Errorf("answer question = %q", last.Content)
	}
}

func TestPresentKeepsSpecificTitle(t *testing.T) {
	rc := &reqCtx{req: Request{Message: "only cash"}, resp: &Response{}}
	if !rc.present(presentedAnswer{
		Summary: "Cash today.",
		Stats:   &presentedFacts{Title: "Cash", Rows: []presentedStat{{Label: "Collected", Value: "12.00"}}},
	}) {
		t.Fatal("present returned false")
	}
	if rc.resp.Cards[0].Title != "Cash" {
		t.Errorf("title = %q, want the model's title", rc.resp.Cards[0].Title)
	}
}

func TestPresentCardsOrderAndCaps(t *testing.T) {
	rows := make([]map[string]any, 0, maxPresentRows+5)
	for i := 0; i < maxPresentRows+5; i++ {
		rows = append(rows, map[string]any{"id": float64(i), "extra": "x"})
	}
	got := presentCards(presentedAnswer{
		List:  &presentedList{Items: []presentedItem{{Title: "Note"}}},
		Table: &presentedTable{Columns: []cards.Column{{Key: "id", Label: "ID"}}, Rows: rows, Total: 120},
		Stats: &presentedFacts{Rows: []presentedStat{{Label: "Arrivals", Value: "4"}, {Label: "", Value: "drop"}}},
		Chart: &presentedChart{Kind: "bar", XKey: "day", Series: []cards.ChartSeries{{Key: "n", Label: "Arrivals"}},
			Data: []map[string]any{{"day": "Mon", "n": float64(2)}, {"day": "Tue", "n": "3"}, {"day": "Wed", "n": "n/a"}}},
	})
	var types []string
	for _, c := range got {
		types = append(types, c.Type)
	}
	if strings.Join(types, ",") != "data,chart,table,list" {
		t.Fatalf("card order = %v", types)
	}
	if n := len(got[0].Rows); n != 1 {
		t.Errorf("stats rows = %d, want blank labels dropped", n)
	}
	if n := len(got[1].Chart.Data); n != 2 {
		t.Errorf("chart points = %d, want the non-numeric point dropped", n)
	}
	tbl := got[2]
	if len(tbl.Table.Rows) != maxPresentRows || tbl.Total != 120 {
		t.Errorf("table rows=%d total=%d", len(tbl.Table.Rows), tbl.Total)
	}
	if _, ok := tbl.Table.Rows[0]["extra"]; ok {
		t.Error("table rows must only hold declared columns")
	}
}

func TestRefusedHotelMetricStillReadsKPIs(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRefuse, Message: "not my job"}),
		{Text: "RevPAR is revenue per available room. This property's figure was not in the data."},
	}}
	rc, seen := plannerFixture(t, llm)
	rc.req.Message = "what is revpar?"

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("hotel term was not answered")
	}
	if rc.resp.Text == scopeRefusal {
		t.Fatal("RevPAR was refused as general knowledge")
	}
	if len(*seen) != 1 || !strings.Contains((*seen)[0].URL.Path, "kpi-overview") {
		t.Fatalf("want the KPI read, got %d calls", len(*seen))
	}
	if got := (*seen)[0].URL.Query().Get("date"); got != "2026-10-20" {
		t.Errorf("date = %q, want the business date", got)
	}
}

func TestRefusedHotelTermIsExplainedWithoutARead(t *testing.T) {
	llm := &scriptedLLM{replies: []genillm.Response{
		planCall(t, readPlan{Action: planActionRefuse}),
		{Text: "A folio is the running bill for a guest's stay."},
	}}
	rc, seen := plannerFixture(t, llm)
	rc.req.Message = "what is a folio?"

	if !rc.e.planAndAnswer(rc) {
		t.Fatal("folio question was not answered")
	}
	if len(*seen) != 0 {
		t.Fatalf("a definition should not call property APIs, got %d", len(*seen))
	}
	if rc.resp.Text != "A folio is the running bill for a guest's stay." {
		t.Errorf("text = %q", rc.resp.Text)
	}
}

func TestHotelConceptDoesNotMatchOrdinaryWords(t *testing.T) {
	if _, ok := hotelConceptRead("what is the address of the hotel?", "2026-10-20"); ok {
		t.Fatal("address must not match ADR")
	}
	read, ok := hotelConceptRead("what is RevPAR?", "2026-10-20")
	if !ok || read.Tool != kpiOverviewTool || read.Query["date"] != "2026-10-20" {
		t.Fatalf("revpar read = %+v ok=%v", read, ok)
	}
	settings, ok := hotelConceptRead("check night audit settings", "2026-10-20")
	if !ok || settings.Tool != "config_get_properties_id_night_audit_settings" {
		t.Fatalf("settings read = %+v ok=%v", settings, ok)
	}
	status, ok := hotelConceptRead("what is the night audit status", "2026-10-20")
	if !ok || status.Tool != "reservation_get_night_audit_status" {
		t.Fatalf("status read = %+v ok=%v", status, ok)
	}
	if !isHotelConcept("what is a no-show?") {
		t.Fatal("no-show is a hotel term")
	}
	if isHotelConcept("what is the capital of France") {
		t.Fatal("trivia matched a hotel term")
	}
}

func TestPresentChartNeedsTwoNumericPoints(t *testing.T) {
	_, ok := chartCard(&presentedChart{Kind: "line", XKey: "d", Series: []cards.ChartSeries{{Key: "v"}},
		Data: []map[string]any{{"d": "2026-10-01", "v": float64(1)}}})
	if ok {
		t.Error("a single point should not become a chart")
	}
}
