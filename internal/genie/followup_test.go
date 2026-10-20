package genie

import (
	"testing"

	genillm "asi-genie-poc/llm"
)

func TestFillFollowUpSlot_lookupGuest(t *testing.T) {
	t.Parallel()
	var s Slots
	if !fillFollowUpSlot("lookup_guest", "Sunny Sahsi", &s) {
		t.Fatal("expected fill")
	}
	if s.GuestName != "Sunny Sahsi" {
		t.Fatalf("guestName = %q", s.GuestName)
	}
}

func TestLooksLikeNewQuestion_rejectsLookupPhrase(t *testing.T) {
	t.Parallel()
	if !looksLikeNewQuestion("Look up a guest") {
		t.Fatal("expected new question")
	}
	if looksLikeNewQuestion("Sunny Sahsi") {
		t.Fatal("name alone should not be a new question")
	}
}

func TestClassifierMessages_newQuestionOmitsHistory(t *testing.T) {
	t.Parallel()
	prior := []genillm.Message{
		{Role: genillm.RoleUser, Content: "Find Johnson"},
		{Role: genillm.RoleAssistant, Content: "Found Maria Johnson in room 112."},
	}
	got := classifierMessages("Who arrives today?", prior)
	if len(got) != 1 || got[0].Content != "Who arrives today?" {
		t.Fatalf("new question included old turns: %#v", got)
	}
}

func TestClassifierMessages_pronounKeepsHistory(t *testing.T) {
	t.Parallel()
	prior := []genillm.Message{
		{Role: genillm.RoleUser, Content: "Find Johnson"},
		{Role: genillm.RoleAssistant, Content: "Found Maria Johnson in room 112."},
	}
	got := classifierMessages("What's her balance?", prior)
	if len(got) != 3 || got[2].Content != "What's her balance?" {
		t.Fatalf("pronoun follow-up dropped context: %#v", got)
	}
}

func TestClassifierMessages_shortReplyKeepsHistory(t *testing.T) {
	t.Parallel()
	prior := []genillm.Message{
		{Role: genillm.RoleAssistant, Content: "Which guest?"},
	}
	got := classifierMessages("Sunny Sahsi", prior)
	if len(got) != 2 || got[1].Content != "Sunny Sahsi" {
		t.Fatalf("clarify reply dropped context: %#v", got)
	}
}

func TestClarifyIntentFromPersistMeta(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"cards":[{"type":"clarify","intent":"lookup_guest"}]}`)
	if got := clarifyIntentFromPersistMeta(raw); got != "lookup_guest" {
		t.Fatalf("intent = %q", got)
	}
}
