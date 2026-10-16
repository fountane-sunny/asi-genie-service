package genie

import "testing"

func TestPropertyIDsOf(t *testing.T) {
	t.Parallel()
	v := []any{
		map[string]any{"propertyId": float64(12), "moduleName": "Reservation"},
		map[string]any{"propertyId": float64(12)},
		map[string]any{"propertyId": float64(0)},
		map[string]any{"propertyId": "4"},
	}
	got := propertyIDsOf(v)
	if len(got) != 2 || got[0] != 12 || got[1] != 4 {
		t.Fatalf("property ids = %v", got)
	}
}

func TestUserInfoAllows(t *testing.T) {
	t.Parallel()
	u := userInfo{propertyIDs: []int{3, 9}}
	if !u.allows(9) {
		t.Fatal("expected membership")
	}
	if u.allows(1) {
		t.Fatal("other property must be refused")
	}
}
