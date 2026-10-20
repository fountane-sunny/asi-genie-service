package genie

import (
	"log/slog"
	"strings"

	genillm "asi-genie-poc/llm"
)

// conceptRule is one hotel term that must not be refused as trivia.
// tool is the read that holds this property's figure; empty means explain only.
type conceptRule struct {
	phrases []string
	tool    string
}

const kpiOverviewTool = "reservation_get_dashboard_kpi_overview"

// Longest phrases first so "rev par" wins over a shorter neighbour.
var conceptRules = []conceptRule{
	{[]string{"revenue per available room", "rev par", "revpar"}, kpiOverviewTool},
	{[]string{"average daily rate", "adr"}, kpiOverviewTool},
	{[]string{"occupancy", "revpor"}, kpiOverviewTool},
	{[]string{"night audit setting", "night audit settings"}, "config_get_properties_id_night_audit_settings"},
	{[]string{"night audit"}, "reservation_get_night_audit_status"},
	{[]string{"cancellation policy"}, "config_get_cancellation_policies"},
	{[]string{
		"folio", "no show", "walk in", "length of stay", "los", "rack rate",
		"out of order", "out of service", "house count", "overbooking",
		"cta", "ctd", "best available rate",
	}, ""},
}

// hotelConceptRead is the property read for a refused "what is RevPAR?" style
// question. ok is false when the term has no live figure, or is not a hotel term.
func hotelConceptRead(message, bizDate string) (plannedRead, bool) {
	rule, ok := matchConcept(message)
	if !ok || rule.tool == "" {
		return plannedRead{}, false
	}
	read := plannedRead{Tool: rule.tool}
	if rule.tool == kpiOverviewTool && len(bizDate) == 10 {
		read.Query = map[string]string{"date": bizDate}
	}
	return read, true
}

func isHotelConcept(message string) bool {
	_, ok := matchConcept(message)
	return ok
}

func matchConcept(message string) (conceptRule, bool) {
	norm := normalizeQuestion(message)
	for _, rule := range conceptRules {
		for _, phrase := range rule.phrases {
			if strings.Contains(norm, " "+phrase+" ") {
				return rule, true
			}
		}
	}
	return conceptRule{}, false
}

func normalizeQuestion(message string) string {
	s := strings.ToLower(message)
	s = strings.NewReplacer("-", " ", "/", " ", "?", " ", "'", "", ",", " ", ".", " ").Replace(s)
	return " " + strings.Join(strings.Fields(s), " ") + " "
}

const hotelTermSystem = `You explain hotel property-management terms to front-desk staff.
Answer in two short sentences: what the term means in a hotel, then how someone uses it on a shift.
Do not invent this property's numbers, guest names, or dates.
If the question is not about hotel operations, say you can only help with this property.`

// explainHotelConcept answers a hotel-term question that has no property read.
func (e *Engine) explainHotelConcept(rc *reqCtx) bool {
	ctx, cancel := rc.llmCtx()
	res, err := e.LLM.SendMessage(ctx, hotelTermSystem, []genillm.Message{{
		Role: genillm.RoleUser, Content: rc.req.Message,
	}}, nil)
	cancel()
	if err != nil {
		rc.llmFailed = true
		slog.Warn("genie hotel-term explanation failed; using intent router", "err", err, "propertyId", rc.pid)
		return false
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		slog.Warn("genie hotel-term explanation was empty", "propertyId", rc.pid)
		return false
	}
	rc.setIntent(IntentInfo{ID: "agent", Handler: "planner", Source: "llm"})
	rc.text(text)
	return true
}
