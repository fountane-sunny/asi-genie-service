package genie

import (
	"encoding/json"
	"testing"

	"asi-genie-poc/toolregistry/generated"
)

func TestForcePropertyPinsPropertyPathAndQuery(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"id":"99","userId":"7","query":{"propertyId":"99","property_id":"99","propertyIds":"1,2","date":"2026-10-01"}}`)
	got := forceProperty("/v1/properties/{id}/hk-assignments", raw, "12")
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["id"] != "12" {
		t.Fatalf("property id = %v", m["id"])
	}
	if m["userId"] != "7" {
		t.Fatalf("userId was rewritten: %v", m["userId"])
	}
	q := m["query"].(map[string]any)
	for _, k := range []string{"propertyId", "property_id", "propertyIds"} {
		if q[k] != "12" {
			t.Fatalf("%s = %v", k, q[k])
		}
	}
	if q["date"] != "2026-10-01" {
		t.Fatalf("date = %v", q["date"])
	}
}

func TestForcePropertyLeavesReservationID(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"id":"res-1","query":{"propertyId":"99","status":"confirmed"}}`)
	got := forceProperty("/api/v1/reservations/{id}", raw, "12")
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["id"] != "res-1" {
		t.Fatalf("reservation id = %v", m["id"])
	}
	q := m["query"].(map[string]any)
	if q["propertyId"] != "12" {
		t.Fatalf("propertyId = %v", q["propertyId"])
	}
	if q["status"] != "confirmed" {
		t.Fatalf("status = %v", q["status"])
	}
}

func TestIsAllowedReadTool(t *testing.T) {
	t.Parallel()
	keep := []generated.ToolMeta{
		{Name: "reservation_get_reservations", Method: "GET", Path: "/api/v1/reservations", ReadOnly: true},
		{Name: "auth_get_current_user", Method: "GET", Path: "/v1/auth/currentUser", ReadOnly: true},
		{Name: "auth_get_permissions", Method: "GET", Path: "/v1/auth/permissions", ReadOnly: true},
		{Name: "config_get_properties_id_hk_assignments", Method: "GET", Path: "/v1/properties/{id}/hk-assignments", ReadOnly: true},
	}
	for _, tool := range keep {
		if !IsAllowedReadTool(tool) {
			t.Fatalf("kept tool was dropped: %s", tool.Name)
		}
	}
	drop := []generated.ToolMeta{
		{Name: "auth_get_admin_users", Method: "GET", Path: "/v1/auth/admin/users", ReadOnly: true},
		{Name: "auth_get_admin_impersonation_active", Method: "GET", Path: "/v1/auth/admin/impersonation/active", ReadOnly: true},
		{Name: "auth_get_invite_token", Method: "GET", Path: "/v1/auth/invite/{token}", ReadOnly: true},
		{Name: "config_get_internal_properties_id_out_of_services_ending_on", Method: "GET", Path: "/v1/internal/properties/{id}/out-of-services/ending-on", ReadOnly: true},
		{Name: "config_get_audit_logs_user", Method: "GET", Path: "/v1/audit-logs/user", ReadOnly: true},
		{Name: "reservation_get_reservations_export", Method: "GET", Path: "/api/v1/reservations/export", ReadOnly: true},
		{Name: "reservation_post_reservations", Method: "POST", Path: "/api/v1/reservations", ReadOnly: false},
	}
	for _, tool := range drop {
		if IsAllowedReadTool(tool) {
			t.Fatalf("blocked tool was kept: %s", tool.Name)
		}
	}
}
