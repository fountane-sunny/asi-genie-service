package genie

import "testing"

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

func TestClarifyIntentFromPersistMeta(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"cards":[{"type":"clarify","intent":"lookup_guest"}]}`)
	if got := clarifyIntentFromPersistMeta(raw); got != "lookup_guest" {
		t.Fatalf("intent = %q", got)
	}
}
