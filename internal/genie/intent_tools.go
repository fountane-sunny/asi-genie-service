package genie

// intentTools is the small, audited read surface for each deterministic intent.
// Tool names are generated registry names, never model-selected endpoint prose.
var intentTools = map[string][]string{
	"todays_arrivals":      {"reservation_get_reservations"},
	"todays_departures":    {"reservation_get_reservations"},
	"property_snapshot":    {"reservation_get_dashboard_today_summary", "reservation_get_dashboard_kpi_overview"},
	"compare_metrics":      {"reservation_get_dashboard_kpi_overview"},
	"occupancy_forecast":   {"reservation_get_inventory_forecasting"},
	"check_availability":   {"config_get_room_types_summary", "config_get_rate_types"},
	"action_items":         {"reservation_get_dashboard_action_items"},
	"shift_notes_read":     {"reservation_get_dashboard_shift_notes"},
	"lookup_reservation":   {"reservation_get_reservations_search", "reservation_get_reservations_id"},
	"lookup_guest":         {"reservation_get_guests", "reservation_get_guests_guest_id", "config_get_properties_id_users"},
	"current_user":         {"auth_get_current_user", "config_get_properties_id_users"},
	"list_rooms":           {"config_get_room_space_statuses", "config_get_rooms"},
	"room_status_summary":  {"config_get_room_statuses_summary"},
	"hk_assignments_read":  {"config_get_properties_id_hk_assignments"},
	"rate_query":           {"config_get_rate_types", "config_get_rate_adjustments"},
	"night_audit_status":   {"reservation_get_night_audit_status"},
	"revenue_query":        {"reservation_get_api_v1_reports_sales_and_revenue"},
	"policy_lookup":        {"config_get_cancellation_policies"},
	"inventory_status":     {"config_get_room_statuses_summary"},
	"channel_distribution": {"reservation_get_dashboard_channel_distribution"},
}

func toolsForIntent(intentID string, slots Slots) []string {
	tools := append([]string(nil), intentTools[intentID]...)
	if intentID == "occupancy_forecast" && slots.RoomType != "" {
		tools = append(tools, "reservation_get_inventory_forecasting_inventory_details")
	}
	return tools
}
