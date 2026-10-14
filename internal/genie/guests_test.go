package genie

import "testing"

func TestDedupeGuestRecords_byID(t *testing.T) {
	t.Parallel()
	list := []map[string]any{
		{"id": "a", "guestName": "Sunny Sahsi", "city": "Ejido"},
		{"id": "a", "guestName": "SUnny Sahsi", "city": "Ejido del Centro"},
		{"id": "b", "guestName": "Sunny Sahsi", "city": "Other"},
	}
	out := dedupeGuestRecords(list)
	if len(out) != 2 {
		t.Fatalf("want 2 unique guests, got %d", len(out))
	}
}

func TestParseGuestIDFromLookupMessage(t *testing.T) {
	t.Parallel()
	msg := "Look up guest Sunny Sahsi (guest id fa0d129a-c993-46f5-8760-ff142cf3e813)"
	got := parseGuestIDFromLookupMessage(msg)
	if got != "fa0d129a-c993-46f5-8760-ff142cf3e813" {
		t.Fatalf("got %q", got)
	}
}

func TestGuestDisambiguationOptions_uniqueLabels(t *testing.T) {
	t.Parallel()
	list := []map[string]any{
		{"id": "11111111-1111-1111-1111-111111111111", "guestName": "Sunny Sahsi", "email": "a@test.com"},
		{"id": "22222222-2222-2222-2222-222222222222", "guestName": "Sunny Sahsi", "email": "b@test.com"},
	}
	opts := guestDisambiguationOptions(list)
	if len(opts) != 2 {
		t.Fatalf("want 2 options, got %d", len(opts))
	}
	if opts[0].Label == opts[1].Label {
		t.Fatalf("labels should differ: %q vs %q", opts[0].Label, opts[1].Label)
	}
}
