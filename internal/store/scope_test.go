package store

import "testing"

func TestValidateScopeRejectsMissingProperty(t *testing.T) {
	t.Parallel()
	if err := ValidateScope(0, "user-1"); err == nil {
		t.Fatal("property id 0 must be rejected, not treated as every property")
	}
	if err := ValidateScope(-1, "user-1"); err == nil {
		t.Fatal("negative property id must be rejected")
	}
	if err := ValidateScope(12, ""); err == nil {
		t.Fatal("missing user id must be rejected")
	}
	if err := ValidateScope(12, "  "); err == nil {
		t.Fatal("blank user id must be rejected")
	}
	if err := ValidateScope(12, "user-1"); err != nil {
		t.Fatalf("valid scope: %v", err)
	}
}
