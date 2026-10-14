#!/usr/bin/env python3
"""Writes docs/phase1-coverage.md from Scenarios.csv + the handler map below.
Keep TOOLS in sync with internal/genie/handlers.go dedicatedHandlers()."""
import csv, re, collections

TOOLS = {
 "property_snapshot": "reservation_get_dashboard_today_summary, reservation_get_dashboard_kpi_overview (only with Dashboard → KPIs READ)",
 "compare_metrics": "reservation_get_dashboard_kpi_overview (×2 or value vs previousValue); delta computed server-side",
 "todays_arrivals": "reservation_get_reservations (checkInDateStart/End = business date)",
 "todays_departures": "reservation_get_reservations (checkoutDateStart/End); 'still checked in' flag from status",
 "check_availability": "config_get_room_types_summary (startDate/endDate); asks for dates when missing",
 "check_availability_missing_dates": "none - clarifying chips built from the business date",
 "lookup_reservation": "reservation_get_reservations_search → reservation_get_reservations_id",
 "reservation_policy_check": "search → reservation_get_reservations_id → config_get_rate_types_id → config_get_cancellation_policies_id",
 "folio_balance_check": "search → reservation_get_reservations_id_payment_summary, reservation_get_reservations_id_payments",
 "quick_checkin": "search → reservation_get_reservations_id_room_status, reservation_get_reservations_id_payment_summary",
 "quick_checkout": "search → reservation_get_reservations_id_payment_summary",
 "late_checkout_quote": "config_get_properties_id_global_settings_summary, reservation_get_reservations (lateCheckout=true)",
 "guide_query": "none - Phase 1 has no help-doc corpus: not-supported + navigate to the screen",
 "lookup_guest": "reservation_get_guests (searchKey) → reservation_get_guests_guest_id, reservation_get_guests_guest_id_reservations",
 "room_status_summary": "config_get_room_statuses_summary",
 "hk_assignments_read": "config_get_properties_id_hk_assignments (+ auth_get_current_user to filter own assignments)",
 "update_room_status": "config_get_room_space_statuses (current status) + deep link",
 "rate_query": "config_get_rate_types (+ config_get_rate_adjustments when dates given)",
 "night_audit_status": "reservation_get_night_audit_status",
 "night_audit_readiness": "unposted-reservations (countOnly), reservations (due-outs in house), dashboard action-items, night-audit hourly-overlaps, today-summary",
 "night_audit_walkthrough": "readiness checks above + deep link to Night Audit",
 "view_report": "none - deep link to /reports with report + dates",
 "revenue_query": "reservation_get_api_v1_reports_sales_and_revenue",
 "policy_lookup": "config_get_cancellation_policies",
 "capabilities": "intent registry (P0 examples)",
 "navigate": "none - route table (asi-frontend ROUTES)",
 "navigate_entity": "reservation_get_reservations_search → deep link",
 "denied_intent": "answers via property_snapshot; ASI 401/403 → Denied card",
 "disambiguate": "pick-list from reservation/guest search",
 "pci_block": "server-side PAN detector (Luhn) before the LLM; masked in store",
 "context_carry": "session last entity (reservation/guest/room)",
 "since_last_shift": "reservations (entryDate), in-house-room-change report, shift-notes, action-items, today-summary",
 "create_reservation": "config_get_room_types_summary (when dates given) + prefilled /reservation/create link; otherwise slot clarify (never books)",
 "create_reservation_slots": "none until all slots known; then config_get_room_types_summary + prefilled /reservation/create link",
 "log_service_request": "config_get_room_space_statuses (when room given) + prefilled task link (Type = Maintenance)",
 "clarify_slot": "none - deterministic date check vs business date; valid date continues to config_get_room_types_summary",
 "late_checkout_fee_tier": "same reads as late_checkout_quote + tier clarify (quotes only, never posts)",
 "property_scope_missing": "none - unreachable as a question: propertyId is required on /chat (400 otherwise); replies with the current property",
}
GAPS = {
 "reservation_policy_check": "Fee amount not computed: cancellation-policy rule schema (fee tiers/deadlines) not yet mapped; card shows the policy rules + days to arrival.",
 "late_checkout_quote": "No late-checkout fee/grace settings endpoint found in the specs; fee is not quoted.",
 "quick_checkin": "ID/document status has no read endpoint mapped; shown as 'unknown'.",
 "check_availability": "Uses room-types-summary; day-availability needs enquiryId/rateTypeId so is not used.",
 "update_room_status": "Housekeeping deep link uses /room-space-status-change (no dedicated housekeeping-task ROUTE constant).",
 "view_report": "Report deep-link query params (report, date) are assumed; reports page must read them.",
 "since_last_shift": "'Last shift' = previous Genie session time (no shift-end API); room status changes not included.",
 "hk_assignments_read": "Own-assignment filter assumes userId on assignment rows matches currentUser id.",
 "log_service_request": "No tasks ROUTE constant in asi-frontend; link assumes /room-space-status-change accepts type/room/issue params.",
 "late_checkout_fee_tier": "No fee-tier settings endpoint; tier is asked but the fee is not computed.",
 "create_reservation": "Prefill params (checkInDate/roomTypeId/guestId) on /reservation/create match ReservationForm searchParams.",
 "create_reservation_slots": "Same ReservationForm prefill keys as create_reservation (checkInDate/roomTypeId/guestId).",
 "compare_metrics": "Ranges (month vs month) compared as single business days; range totals need a revenue-by-range endpoint.",
}
rows = [r for r in csv.DictReader(open("specs/catalogue/genie-catalogue/Scenarios.csv", encoding="utf-8-sig")) if r["Phase 1 scope"].strip().startswith(("In", "Partial"))]
def handler(r):
    i = r["Intent / capability id"].strip(); rt = r["Response type"].strip()
    if i in TOOLS: return "dedicated"
    if rt.startswith("Navigate"): return "navigate"
    if rt == "Guide + citation": return "not-supported + navigate (no docs corpus)"
    if rt == "Not supported (captured)": return "not-supported (captured)"
    if rt in ("Denied (RBAC)", "Clarifying question", "Disambiguation"): return "fallback card"
    return "generic (LLM picks 1 GET tool, server renders card)"
out = ["# Phase 1 coverage", "", "Generated by `scripts/gen-intents/gen_coverage.py` from `specs/catalogue/genie-catalogue/Scenarios.csv` (Phase 1 scope starting In… or Partial…, e.g. 'Partial (prefill)').", ""]
cnt = collections.Counter(handler(r) for r in rows)
p0 = [r for r in rows if r["Priority"] == "P0"]
out += [f"- Scenarios: **{len(rows)}** ({len(set(r['Intent / capability id'] for r in rows))} capability ids); P0: **{len(p0)}**, all dedicated: **{all(handler(r)=='dedicated' for r in p0)}**",
        "- Handlers: " + ", ".join(f"{k}: {v}" for k, v in cnt.most_common()), "",
        "Generic = for P1/P2 data/list/comparison/checklist intents the LLM chooses one read endpoint from the top-12 GET tools for the message; the server renders the card (no LLM math). Non-GET tools are never offered.", "",
        "| ID | Capability | Pri | Scope | Response type | Handler | Tool(s) | Gap |", "|---|---|---|---|---|---|---|---|"]
for r in rows:
    i = r["Intent / capability id"].strip()
    out.append("| {} | `{}` | {} | {} | {} | {} | {} | {} |".format(r["ID"], i, r["Priority"], r["Phase 1 scope"], r["Response type"], handler(r),
               TOOLS.get(i, "selected at runtime" if handler(r).startswith("generic") else "-").replace("|", "/"), GAPS.get(i, "").replace("|", "/")))
out += ["", "## Known gaps (cross-cutting)", "",
        "- Reservation Swagger path keys are normalized at codegen (`swaggerToolPath`) and again in the HTTP client (`joinURL`) so `ASI_RESERVATION_BASE_URL` ending in `/api/v1` does not double the prefix.",
        "- Response field names for list endpoints are read defensively (`data`/`items`/`rows`, `guestName`/`firstName`+`lastName`, ...) because most list responses are untyped in Swagger; verify against dev data.",
        "- RBAC is enforced by the ASI services: every call carries the user's token, and a 401/403 becomes a `denied` card. Genie fetches no permissions itself.",
        "- Requirements written as alternatives in the catalogue (`A / B`, `(or …)`) are treated as optional extras.", ""]
open("docs/phase1-coverage.md", "w").write("\n".join(out))
print("ok", len(rows), cnt)
