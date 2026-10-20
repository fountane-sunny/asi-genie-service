package intents

import "testing"

func TestCorrectSpellingSnapsTyposToVocabulary(t *testing.T) {
	cases := map[string]string{
		"list all the roroms":         "list all the rooms",
		"list all the roomw":          "list all the rooms",
		"list all the available toom": "list all the available toom", // first letter differs: left alone
		"arivals tday":                "arrivals today",
		"housekeping tasks":           "housekeeping tasks",
		"ocupancy this weakend":       "occupancy this weekend",
	}
	for in, want := range cases {
		got, _ := CorrectSpelling(in)
		if got != want {
			t.Errorf("CorrectSpelling(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCorrectSpellingLeavesNamesAndNumbers(t *testing.T) {
	for _, in := range []string{"who is sunny sahsi", "room 201", "reservation 48213", "find guest maria johnson"} {
		got, changed := CorrectSpelling(in)
		if changed || got != in {
			t.Errorf("CorrectSpelling(%q) changed to %q", in, got)
		}
	}
}
