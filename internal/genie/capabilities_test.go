package genie

import (
	"strings"
	"testing"
)

func TestCapabilitySuggestionsAreDoableAndVary(t *testing.T) {
	t.Parallel()
	first := capabilitySuggestions(capabilitySuggestionCount)
	if len(first) != capabilitySuggestionCount {
		t.Fatalf("count=%d", len(first))
	}
	seen := map[string]bool{}
	for _, item := range first {
		if item.Title == "" || item.Subtitle == "" {
			t.Fatalf("item=%+v", item)
		}
		if undoableExample.MatchString(item.Title) {
			t.Fatalf("not doable: %s", item.Title)
		}
		lower := strings.ToLower(item.Title)
		for _, banned := range []string{"book a", "smith", "4471", "check in", "room 210"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("suggestion %q is not something Genie can do for any property", item.Title)
			}
		}
		seen[item.Title] = true
	}
	varied := false
	for i := 0; i < 20; i++ {
		next := capabilitySuggestions(capabilitySuggestionCount)
		if next[0].Title != first[0].Title {
			varied = true
			break
		}
	}
	if !varied {
		t.Fatal("suggestions stayed in the same order")
	}
	if len(seen) != len(first) {
		t.Fatal("a single sample repeated a question")
	}
}
