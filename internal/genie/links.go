package genie

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"asi-genie-poc/internal/cards"
)

// Deep links mirror asi-frontend/apps/web/app/constants.ts ROUTES.
// Routes marked "assumed" have no ROUTES constant yet (see docs/phase1-coverage.md).

func p(pid int, suffix string) string { return fmt.Sprintf("/property/%d%s", pid, suffix) }

type routeDef struct {
	label   string
	suffix  string // appended to /property/{id}
	module  string
	feature string
	words   []string
}

var routeTable = []routeDef{
	{"Open dashboard", "/dashboard", "Dashboard", "Today's at a Glance", []string{"dashboard", "home", "glance", "kpi"}},
	{"Open shift notes", "/dashboard/shift-notes", "Dashboard", "Shift Notes", []string{"shift note", "handover"}},
	{"Open new reservation", "/reservation/create", "Reservation", "Reservation", []string{"new reservation", "create reservation", "book", "new booking"}},
	{"Search reservations", "/reservation/search", "Reservation", "Reservation", []string{"search reservation", "find reservation"}},
	{"Open reservations", "/reservation", "Reservation", "Reservation", []string{"reservation", "booking", "calendar", "arrival", "departure"}},
	{"Open group reservations", "/group-reservation", "Guest", "Group Database", []string{"group"}},
	{"Open guest database", "/guests/database", "Guest", "Guest Database", []string{"guest database", "guests", "guest profile"}},
	{"Open guest ledger", "/guest-ledger", "Front Desk", "Guest Ledger", []string{"guest ledger", "ledger", "folio"}},
	{"Open batch folio", "/batch-folio", "Front Desk", "Batch Folio", []string{"batch folio"}},
	{"Open late check-out", "/late-check-out", "Front Desk", "Late Checkout", []string{"late check", "late checkout"}},
	{"Open room status", "/room-space-status-change", "Housekeeping Management", "Housekeeping Tasks", []string{"room status", "housekeeping", "clean", "dirty"}},
	{"Open night audit", "/night-audit", "Audit", "Night Audit", []string{"night audit", "audit run"}},
	{"Open CRS bulk sync", "/crs-bulk-sync", "Audit", "CRS Bulk Sync", []string{"crs", "sync", "ota"}},
	{"Open reports", "/reports", "Reports", "", []string{"open reports", "go to reports", "reports page"}},
	{"Open audit log", "/audit-log", "Settings", "Global", []string{"audit log", "audit trail", "who changed"}},
	{"Open email templates", "/email-templates", "Settings", "Email", []string{"email template"}},
	{"Open folio notices", "/folio-notices", "Settings", "Global", []string{"folio notice"}},
	{"Open property settings", "/edit", "Property Configuration", "Property Overview", []string{"setting", "configuration", "property detail", "rate", "tax", "room type", "amenit"}},
	{"Open property", "", "Property Configuration", "Property Overview", []string{"property"}},
}

// findRoute maps a free-text destination to a route definition.
func findRoute(dest string) (routeDef, bool) {
	d := strings.ToLower(dest)
	if d == "" {
		return routeDef{}, false
	}
	for _, r := range routeTable {
		for _, w := range r.words {
			if strings.Contains(d, w) {
				return r, true
			}
		}
	}
	return routeDef{}, false
}

func (r routeDef) link(pid int, params map[string]string) cards.Link {
	return cards.Link{Label: r.label, Route: p(pid, r.suffix), Params: params}
}

// moduleLink is the default "take me there" for a catalogue module.
func moduleLink(module string, pid int) cards.Link {
	if pid == 0 {
		return cards.Link{Label: "Open ASI", Route: "/"}
	}
	switch module {
	case "Dashboard", "Analytics & Insights", "Proactive (Phase 2+)":
		return cards.Link{Label: "Open dashboard", Route: p(pid, "/dashboard")}
	case "Reservation":
		return cards.Link{Label: "Open reservations", Route: p(pid, "/reservation")}
	case "Front Desk":
		return cards.Link{Label: "Open guest ledger", Route: p(pid, "/guest-ledger")}
	case "Guest":
		return cards.Link{Label: "Open guest database", Route: p(pid, "/guests/database")}
	case "Housekeeping":
		return cards.Link{Label: "Open room status", Route: p(pid, "/room-space-status-change")}
	case "Audit":
		return cards.Link{Label: "Open night audit", Route: p(pid, "/night-audit")}
	case "Reports":
		return cards.Link{Label: "Open reports", Route: p(pid, "/reports")}
	case "Business Channels", "Communication":
		return cards.Link{Label: "Open property settings", Route: p(pid, "/edit")}
	}
	return cards.Link{Label: "Open property", Route: p(pid, "")}
}

func reservationLink(pid int, id string) *cards.Link {
	if id == "" {
		return nil
	}
	return &cards.Link{Label: "Open reservation", Route: p(pid, "/reservation/"+id)}
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// reservationCreatePrefillParams matches asi-frontend ReservationForm searchParams:
// checkInDate, checkOutDate, roomTypeId, guestId.
func reservationCreatePrefillParams(checkInDate, checkOutDate, roomTypeID, guestID string) map[string]string {
	params := map[string]string{}
	if checkInDate != "" {
		params["checkInDate"] = checkInDate
	}
	if checkOutDate != "" && checkOutDate != checkInDate {
		params["checkOutDate"] = checkOutDate
	}
	if roomTypeID != "" {
		params["roomTypeId"] = roomTypeID
	}
	if guestID != "" {
		params["guestId"] = guestID
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

func guestIDFromSession(rc *reqCtx) string {
	if rc.sess.LastEntity != nil && rc.sess.LastEntity.Kind == "guest" && rc.sess.LastEntity.ID != "" {
		return rc.sess.LastEntity.ID
	}
	return ""
}

func matchRoomTypeID(items []map[string]any, roomTypeName string) string {
	if roomTypeName == "" {
		return ""
	}
	want := strings.TrimSpace(strings.ToLower(roomTypeName))
	for _, m := range items {
		name := strings.TrimSpace(strings.ToLower(str(m, "roomTypeName", "name", "roomType", "title")))
		if name != "" && name == want {
			return str(m, "roomTypeId", "id")
		}
	}
	return ""
}

func reservationCreateLink(rc *reqCtx, checkInDate, checkOutDate, roomTypeName string, availability []map[string]any) cards.Link {
	roomTypeID := matchRoomTypeID(availability, roomTypeName)
	if roomTypeID == "" && roomTypeName != "" {
		if _, err := strconv.ParseInt(strings.TrimSpace(roomTypeName), 10, 64); err == nil {
			roomTypeID = strings.TrimSpace(roomTypeName)
		}
	}
	return cards.Link{
		Label:  "Open new reservation",
		Route:  p(rc.pid, "/reservation/create"),
		Params: reservationCreatePrefillParams(checkInDate, checkOutDate, roomTypeID, guestIDFromSession(rc)),
	}
}
