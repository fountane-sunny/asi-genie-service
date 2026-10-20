package genie

import (
	"fmt"

	"asi-genie-poc/internal/cards"
)

// Write kinds the create/update guide tells apart. Both land a user on a real
// ASI screen instead of a path the model guesses.
const (
	writeKindCreate = "create"
	writeKindUpdate = "update"
)

// createGuide names one create/update write Genie can't perform, and the real
// ASI screen that does it. The model names the entity (by id, in plan_reads)
// and the write kind from the user's question - the same NLU that already
// reads intent through typos elsewhere in the planner - and this table only
// supplies the verified route and copy. Every path takes the propertyId.
//
// Most entities edit in place on the same screen they're added from
// (addPath/addLabel cover both). A few (email templates, folio notices,
// roles) open a numbered record to edit, which Genie has no id for from a
// generic "update X" question, so editPath/editLabel point at the list
// screen instead; set only when it differs from the add screen.
type createGuide struct {
	id        string // plan_reads "entity" enum value
	entity    string // used in the message, e.g. "a room type"
	addTitle  string // navigate card title for create, e.g. "Add a room type"
	addLabel  string // link button text for create, e.g. "Open room types"
	addPath   string
	editTitle string // navigate card title for update; defaults to addTitle
	editLabel string // link button text for update; defaults to addLabel
	editPath  string // defaults to addPath (same screen edits in place)
	canUpdate bool   // false when "update" has no generic (id-less) answer here
	readTool  string // planner-index read the refusal can offer instead
	what      string // what readTool shows, e.g. "the current room types"
}

// createGuideTable mirrors the tabs on the property details screen
// (asi-frontend PropertyTabs.tsx), the email template and folio notice list
// screens, and the dedicated create screens for reservations, guests and
// roles (asi-frontend constants.ts ROUTES).
//
// Reservations and guests are create-only here: updating a specific one
// needs its id, which the existing guest/reservation lookup handlers already
// resolve and link to - a generic "update a reservation" question falls back
// to the model's own message rather than guessing which one.
//
// Users and group reservations are left out entirely: neither has a verified
// stand-alone create screen in asi-frontend, so a guessed link would repeat
// the bug this table fixes.
var createGuideTable = []createGuide{
	{id: "reservation", entity: "a reservation", addTitle: "New reservation", addLabel: "Open new reservation",
		addPath: "/property/%d/reservation/create", canUpdate: false,
		readTool: "reservation_get_reservations", what: "the current reservations"},
	{id: "guest", entity: "a guest", addTitle: "New guest", addLabel: "Open guest form",
		addPath: "/property/%d/guests/create", canUpdate: false,
		readTool: "reservation_get_guests", what: "the guest list"},
	{id: "room_type", entity: "a room type", addTitle: "Add a room type", addLabel: "Open room types",
		addPath: "/property/%d?tab=room-types", canUpdate: true,
		readTool: "config_get_room_types", what: "the current room types"},
	{id: "rate_type", entity: "a rate type", addTitle: "Add a rate type", addLabel: "Open rate types",
		addPath: "/property/%d?tab=rate-types", canUpdate: true,
		readTool: "config_get_rate_types", what: "the current rate types"},
	{id: "amenity", entity: "an amenity", addTitle: "Add an amenity", addLabel: "Open amenities",
		addPath: "/property/%d?tab=amenities", canUpdate: true},
	{id: "tax", entity: "a tax", addTitle: "Add a tax", addLabel: "Open taxes",
		addPath: "/property/%d?tab=taxes", canUpdate: true},
	{id: "other_charge", entity: "an other charge", addTitle: "Add an other charge", addLabel: "Open other charges",
		addPath: "/property/%d?tab=other-charges", canUpdate: true},
	{id: "room", entity: "a room", addTitle: "Add a room", addLabel: "Open rooms",
		addPath: "/property/%d?tab=rooms", canUpdate: true,
		readTool: "config_get_rooms", what: "the current rooms"},
	{id: "space", entity: "a space", addTitle: "Add a space", addLabel: "Open spaces",
		addPath: "/property/%d?tab=spaces", canUpdate: true},
	{id: "building_floor", entity: "a building or floor", addTitle: "Add a building or floor", addLabel: "Open buildings and floors",
		addPath: "/property/%d?tab=buildings-and-floors", canUpdate: true},
	{id: "role", entity: "a role", addTitle: "Add a role", addLabel: "Open roles & privileges",
		addPath: "/property/%d/roles-privileges/create", canUpdate: true,
		editTitle: "Roles & privileges", editLabel: "Open roles & privileges",
		editPath: "/property/%d/roles-privileges",
		readTool: "config_get_roles", what: "the current roles"},
	{id: "email_template", entity: "an email template", addTitle: "New email template", addLabel: "Open email templates",
		addPath: "/property/%d/email-templates/new", canUpdate: true,
		editTitle: "Email templates", editLabel: "Open email templates",
		editPath: "/property/%d/email-templates"},
	{id: "folio_notice", entity: "a folio notice", addTitle: "New folio notice", addLabel: "Open folio notices",
		addPath: "/property/%d/folio-notices/new", canUpdate: true,
		editTitle: "Folio notices", editLabel: "Open folio notices",
		editPath: "/property/%d/folio-notices"},
}

var createGuideByID = func() map[string]createGuide {
	m := make(map[string]createGuide, len(createGuideTable))
	for _, g := range createGuideTable {
		m[g.id] = g
	}
	return m
}()

// createGuideEntityEnum is the plan_reads "entity" enum: the ids the model
// may pick when action=refuse is a create/update for one of these screens.
func createGuideEntityEnum() []any {
	out := make([]any, 0, len(createGuideTable))
	for _, g := range createGuideTable {
		out = append(out, g.id)
	}
	return out
}

// matches reports whether this guide has a generic (id-less) answer for the
// requested write kind - true for every create, and for update only where
// editing happens without first looking up a specific record.
func (g createGuide) matches(writeKind string) bool {
	return writeKind != writeKindUpdate || g.canUpdate
}

func (g createGuide) link(writeKind string, pid int) cards.Link {
	label, path := g.addLabel, g.addPath
	if writeKind == writeKindUpdate && g.editPath != "" {
		label, path = g.editLabel, g.editPath
	}
	return cards.Link{Label: label, Route: fmt.Sprintf(path, pid)}
}

func (g createGuide) title(writeKind string) string {
	if writeKind == writeKindUpdate && g.editTitle != "" {
		return g.editTitle
	}
	return g.addTitle
}

// message is the refusal sentence: what Genie can't do, where it's really
// done, and the read it can offer instead, when one exists.
func (g createGuide) message(writeKind string) string {
	verb, screen := "create", g.addLabel
	action := "add one"
	if writeKind == writeKindUpdate {
		verb, action = "update", "edit one"
		if g.editLabel != "" {
			screen = g.editLabel
		}
	}
	if g.readTool == "" {
		return fmt.Sprintf("I can't %s %s from chat - use %s to %s.", verb, g.entity, screen, action)
	}
	return fmt.Sprintf("I can't %s %s from chat - use %s to %s. I can show you %s if that helps.", verb, g.entity, screen, action, g.what)
}
