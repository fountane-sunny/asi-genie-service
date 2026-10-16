package genie

import (
	"strings"
	"testing"

	"asi-genie-poc/toolregistry/generated"
)

func hkRows() []map[string]any {
	return []map[string]any{
		{
			"user": map[string]any{"userId": "u-1", "firstName": "Ada", "lastName": "Lovelace", "roleName": "Housekeeper"},
			"assignments": []any{
				map[string]any{"name": "201"},
				map[string]any{"name": "202"},
			},
		},
		{
			"user":        map[string]any{"userId": "u-2", "firstName": "Grace", "lastName": "Hopper"},
			"assignments": []any{map[string]any{"name": "305"}},
		},
	}
}

func TestHKAssignmentNamesAndRoleFilter(t *testing.T) {
	t.Parallel()
	title, items := hkAssignmentItems(hkRows(), "Housekeeper", "u-1")
	if title != "My assignments" || len(items) != 1 {
		t.Fatalf("housekeeper view = %q %d items", title, len(items))
	}
	if items[0].Title != "Ada Lovelace" || items[0].Subtitle != "201, 202" || items[0].ID != "u-1" {
		t.Fatalf("row = %+v", items[0])
	}

	title, items = hkAssignmentItems(hkRows(), "Supervisor", "u-1")
	if title != "Housekeeping assignments" || len(items) != 2 {
		t.Fatalf("supervisor view = %q %d items", title, len(items))
	}
	if items[1].Title != "Grace Hopper" || items[1].Subtitle != "305" {
		t.Fatalf("second row = %+v", items[1])
	}

	for _, role := range []string{"", "Front Desk", "housekeeper"} {
		_, items = hkAssignmentItems(hkRows(), role, "u-2")
		if len(items) != 1 || items[0].ID != "u-2" {
			t.Fatalf("role %q saw %+v", role, items)
		}
	}
	_, items = hkAssignmentItems(hkRows(), "", "")
	if len(items) != 0 {
		t.Fatalf("missing caller saw %d rows", len(items))
	}
}

func TestPermissionsDescriptionOverride(t *testing.T) {
	t.Parallel()
	tool := toLLMTool(generated.ToolMeta{
		Name: "auth_get_permissions", Method: "GET", Path: "/v1/auth/permissions",
		Description: "Get user sessions", ReadOnly: true,
	})
	if strings.Contains(strings.ToLower(tool.Description), "session") {
		t.Fatalf("description still talks about sessions: %s", tool.Description)
	}
	if !strings.Contains(strings.ToLower(tool.Description), "permission rows") {
		t.Fatalf("description = %s", tool.Description)
	}
	other := toLLMTool(generated.ToolMeta{
		Name: "reservation_get_reservations", Method: "GET", Path: "/api/v1/reservations",
		Description: "List reservations", ReadOnly: true,
	})
	if !strings.Contains(other.Description, "List reservations") {
		t.Fatalf("other description = %s", other.Description)
	}
}

func TestPageCount(t *testing.T) {
	t.Parallel()
	full := map[string]any{"data": []any{map[string]any{}, map[string]any{}}}
	total, known, note := pageCount(full, 50, 50, 10)
	if known || total != 50 || note != "showing the first 10" {
		t.Fatalf("full page without total = %d known=%v %q", total, known, note)
	}

	withTotal := map[string]any{"total": float64(120), "data": []any{}}
	total, known, note = pageCount(withTotal, 50, 50, 10)
	if !known || total != 120 || note != "showing the first 10" {
		t.Fatalf("api total = %d known=%v %q", total, known, note)
	}

	short := map[string]any{"data": []any{map[string]any{}}}
	total, known, note = pageCount(short, 3, 50, 3)
	if !known || total != 3 || note != "" {
		t.Fatalf("short page = %d known=%v %q", total, known, note)
	}
}

func TestApplySessionNames(t *testing.T) {
	t.Parallel()
	rows := []map[string]any{
		{"userId": "u-1", "deviceName": "Front desk"},
		{"userId": "u-2", "deviceName": "Lobby"},
	}
	lookup := []map[string]any{{"userId": "u-2", "firstName": "Grace", "lastName": "Hopper"}}
	applySessionNames(rows, "u-1", "Ada Lovelace", lookup)
	if rows[0]["name"] != "Ada Lovelace" || rows[1]["name"] != "Grace Hopper" {
		t.Fatalf("names = %v %v", rows[0]["name"], rows[1]["name"])
	}
	if personName(rows[0]) != "Ada Lovelace" {
		t.Fatalf("card name = %s", personName(rows[0]))
	}
}
