package genie

import "testing"

func TestRouteDef_navigateCard_titleNotDuplicateAction(t *testing.T) {
	t.Parallel()
	r, ok := findRoute("open reports")
	if !ok {
		t.Fatal("expected reports route")
	}
	c := r.navigateCard("navigate", 1, nil)
	if c.Link == nil {
		t.Fatal("expected link")
	}
	if c.Link.Label != navigateActionGoThere {
		t.Fatalf("link label = %q, want %q", c.Link.Label, navigateActionGoThere)
	}
	if c.Title == c.Link.Label {
		t.Fatalf("title should differ from link label, both %q", c.Title)
	}
}
