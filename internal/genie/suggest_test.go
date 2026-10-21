package genie

import (
	"context"
	"strings"
	"testing"

	"asi-genie-poc/internal/cards"
	genillm "asi-genie-poc/llm"
)

func suggestionFixture(llm genillm.LLMClient) *reqCtx {
	e := New(llm, logOnlyStore{})
	return &reqCtx{
		e: e, ctx: context.Background(), pid: 42,
		req:  Request{Message: "yes, send it", PropertyID: 42},
		resp: &Response{Text: "Sent the folio for John Smith, reservation 4471, to poc@example.com."},
	}
}

func TestAttachSuggestionsAddsActionsTheUserCanRun(t *testing.T) {
	t.Setenv("GENIE_SUGGESTIONS", "true")
	llm := &scriptedLLM{replies: []genillm.Response{
		toolCall(t, suggestTool, suggestedNext{Suggestions: []suggestedAction{
			{Label: "Download the folio", Message: "Download the folio for John Smith"},
			{Label: "Show the balance", Message: "What's the balance for reservation 4471?"},
			{Label: "Refund it", Message: "Refund the guest"},
			{Label: "Send again", Message: "yes, send it"},
		}}),
	}}
	rc := suggestionFixture(llm)

	rc.attachSuggestions()

	if len(rc.resp.Chips) != 2 {
		t.Fatalf("chips = %+v", rc.resp.Chips)
	}
	if rc.resp.Chips[0].Message != "Download the folio for John Smith" || rc.resp.Chips[1].Message != "What's the balance for reservation 4471?" {
		t.Fatalf("chips = %+v", rc.resp.Chips)
	}
}

func TestAttachSuggestionsLeavesAConfirmAlone(t *testing.T) {
	t.Setenv("GENIE_SUGGESTIONS", "true")
	llm := &scriptedLLM{replies: []genillm.Response{
		toolCall(t, suggestTool, suggestedNext{Suggestions: []suggestedAction{
			{Label: "Arrivals", Message: "Who arrives today?"},
		}}),
	}}
	rc := suggestionFixture(llm)
	rc.resp.Cards = []cards.Card{{Type: cards.TypeClarify, Title: "Email the folio — send it?"}}

	rc.attachSuggestions()

	if len(llm.calls) != 0 || len(rc.resp.Chips) != 0 {
		t.Fatalf("calls = %d chips = %+v", len(llm.calls), rc.resp.Chips)
	}
}

func TestAttachSuggestionsKeepsHandlerChips(t *testing.T) {
	t.Setenv("GENIE_SUGGESTIONS", "true")
	llm := &scriptedLLM{}
	rc := suggestionFixture(llm)
	rc.chips("Who's departing today?")

	rc.attachSuggestions()

	if len(llm.calls) != 0 || len(rc.resp.Chips) != 1 || rc.resp.Chips[0].Message != "Who's departing today?" {
		t.Fatalf("chips = %+v calls = %d", rc.resp.Chips, len(llm.calls))
	}
}

func TestPresentCarriesSuggestions(t *testing.T) {
	t.Setenv("GENIE_SUGGESTIONS", "true")
	rc := &reqCtx{req: Request{Message: "who is in house?"}, resp: &Response{}}
	if !rc.present(presentedAnswer{
		Summary: "12 guests are in house.",
		Suggestions: []suggestedAction{
			{Label: "Arrivals", Message: "Who arrives today?"},
			{Label: "Refund", Message: "Refund the guest"},
		},
	}) {
		t.Fatal("present rejected the answer")
	}
	if len(rc.resp.Chips) != 1 || !strings.Contains(rc.resp.Chips[0].Message, "arrives today") {
		t.Fatalf("chips = %+v", rc.resp.Chips)
	}
}

func TestSuggestionsStayOffUnlessConfigured(t *testing.T) {
	t.Setenv("GENIE_SUGGESTIONS", "false")
	llm := &scriptedLLM{replies: []genillm.Response{
		toolCall(t, suggestTool, suggestedNext{Suggestions: []suggestedAction{
			{Label: "Arrivals", Message: "Who arrives today?"},
		}}),
	}}
	rc := suggestionFixture(llm)

	rc.attachSuggestions()
	if !rc.present(presentedAnswer{
		Summary:     "12 guests are in house.",
		Suggestions: []suggestedAction{{Label: "Arrivals", Message: "Who arrives today?"}},
	}) {
		t.Fatal("present rejected the answer")
	}

	if len(llm.calls) != 0 || len(rc.resp.Chips) != 0 {
		t.Fatalf("calls = %d chips = %+v", len(llm.calls), rc.resp.Chips)
	}
	props, _ := presentToolDef().Parameters["properties"].(map[string]any)
	if _, ok := props["suggestions"]; ok {
		t.Fatal("present_answer should not ask for suggestions while GENIE_SUGGESTIONS is off")
	}
	if strings.Contains(answerPrompt(), "set suggestions") {
		t.Fatal("answer prompt should not ask for suggestions while GENIE_SUGGESTIONS is off")
	}
}
