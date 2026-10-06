package page

import (
	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/web/data/component"
	"github.com/armada/orbital/internal/web/data/layout"
)

// Generic is the list page for ANY ConfigItem type.
//
// Nothing here is per-type: the columns come from the view's derived field
// list, so defining a type in DGraph yields a working list page with no code.
type Generic struct {
	layout.Base
	PageTitle string
	View      configitems.View
	Rows      []map[string]any

	// Columns are the scalar columns the table renders. For a concrete view
	// this is just View.Display; for an INTERFACE view it is the union of the
	// interface's scalars and every implementation's own, so a provider-
	// specific column (an EKSA cluster's clusterType) is not lost just because
	// another implementation has no such field.
	Columns []ColumnHeader

	// RefColumns are the view's single relationships as link columns.
	RefColumns []RefHeader

	// ComputedColumns are the `column:` paths — a value fetched from the end of
	// a path, keyed in each Row by that path.
	ComputedColumns []ColumnHeader

	// FilterBy, when set, is the filter dropdown this page offers.
	FilterBy *FilterBy

	// Total is how many of this type exist, which is not how many Rows holds:
	// the query is capped. Carried for API consumers too — a client paging this
	// data needs the denominator.
	Total int

	// Truncated is whether the cap actually bit. False when everything fits,
	// which is the common case and renders nothing.
	Truncated bool

	// RowCapSetting names the env var that raises the cap, so the notice tells
	// an operator what to change rather than leaving them to find it. Differs
	// per app — orb renders the same page from the same template.
	RowCapSetting string

	// Unavailable, when set, replaces the table with a stated reason. An empty
	// table would say "there are none of these", which is a different claim
	// from "orbital could not look".
	Unavailable string

	// Create, when non-nil, is the New-node form for this type. Nil means the
	// page offers no create button — an interface view, or a type whose orbId
	// orbital does not mint.
	Create *CreateForm
}

// FilterBy is a list page's filter dropdown, resolved server-side.
//
// Both the column position and the option list are computed here rather than in
// JavaScript. Orbital's UI is a consumer of orbital's API like any other, so
// anything it needs to draw this control an integrator needs too — and "walk
// the rendered rows collecting distinct values" is precisely the kind of client
// re-implementation the export-preview flattening was about.
type FilterBy struct {
	// Label is the column heading, so the control names what it filters.
	Label string
	// All is the empty option's text — the column heading pluralised, because
	// it names the whole set rather than one cell.
	All string
	// Column is the 0-based DataTables column index. The table renders Name
	// first, then scalar columns, then reference columns, then Orb ID.
	Column int
	// Options are the distinct values present in that column, sorted. Built
	// from the rows actually rendered — a value that is not on the page cannot
	// be filtered to, and offering it would give an empty table with no
	// explanation.
	Options []string
}

// GenericDetail is the detail page for ANY ConfigItem type.
type GenericDetail struct {
	layout.Base
	PageTitle string
	CanMutate bool

	// HasEditable is whether the editor has ANYTHING to write — the type's own
	// fields OR an owned child's. Gating on the type's own fields hid the Edit
	// button on a wrapper like ClusterBackup, which has no scalars of its own
	// but whose etcd/velero/s3Sync children are exactly what you edit there.
	HasEditable bool

	// CanDelete gates the Delete button. Same permission as editing: the delete
	// machinery itself (button attributes, global handler, modal) is already
	// generic, so only the markup was missing.
	CanDelete   bool
	EditModal   component.EditModal
	View        configitems.View
	OrbID       string
	Entity      map[string]any
	Tabs        []GenericTab
	Unavailable string

	// DomID is the orbId made id-safe — the suffix every per-page element id
	// is built from, so two detail fragments open at once cannot collide.
	DomID string

	// AuditPanelID is the id of the div the audit panel loads into, and the
	// value JS matches to find it. Per-page because a list page can have
	// several detail fragments open at once as tabs.
	AuditPanelID string

	// RelatedOrbIDsCSV is this entity's orbId plus every orbId it owns. A
	// change to an owned child records the CHILD's orbId and never the
	// parent's, so an audit query for the parent alone answers "nothing here"
	// while its iDRAC settings are being rewritten.
	RelatedOrbIDsCSV string

	// FieldValuesJSON carries the entity's RAW editable values, keyed by field,
	// for the proposed-change marks. Raw rather than rendered because a mark is
	// suppressed when a proposal already matches the current value, and
	// comparing a proposed `5` against the string "5" the table shows would be
	// a guess. Only EDITABLE fields: a mark on a field nobody can propose could
	// never fire.
	FieldValuesJSON string

	// FieldRows is the detail page's field table, labels and values resolved.
	FieldRows []FieldRow

	// Meta is the provenance box, precomputed. Label, formatting decision and
	// value all resolved server-side: per the API-first rule the response
	// carries what the view needs, and any walking the template does here an
	// integrator would have to re-implement.
	Meta []MetaRow

	// ListSlug and ListLabel name the list page this detail belongs to. They
	// differ from View when the item was reached through an INTERFACE view:
	// an EKSA cluster opened from /clusters links back to /clusters, not to
	// the concrete type's own page.
	ListSlug  string
	ListLabel string

	// Refs are the entity's SINGLE relationships to things it does not own —
	// a cluster's data centre, its management cluster, its control-plane
	// endpoint. They render as rows in the field list, as links, because that
	// is what every hand-written page did with them. A box per single
	// relationship would put five one-row tables on a cluster page.
	Refs []GenericRef

	// Owned are single relationships the entity DOES own, each rendered as its
	// own box. An owned child carries real data and is edited inline through
	// the parent, so a link to it would be a worse answer than showing it.
	Owned []GenericOwned

	// Actions gates page chrome the two apps differ on — orb has no audit log.
	Actions layout.PageActions

	// RefColumnsDropped records that reference columns were omitted after a
	// query error — almost always a dangling edge. The page still renders; this
	// says why it is missing a column.
	RefColumnsDropped bool
}

// GenericTab is one relationship rendered as a tab of related entities.
type GenericTab struct {
	Label  string
	Slug   string
	Field  string
	IsList bool

	// Columns are the related type's display fields, so a relationship renders
	// as a table of the child's own data rather than a list of names.
	Columns []ColumnHeader

	// RefColumns are the child type's single relationships, rendered as link
	// columns — a server's rack and OOB IP, one hop away.
	RefColumns []RefHeader

	// ComputedColumns are the child type's `column:` paths, keyed in each Row
	// by path — the same columns its own list page shows.
	ComputedColumns []ColumnHeader

	Rows []map[string]any

	// Editable marks which of the child type's fields can be proposed against,
	// so a cell holding one carries a mark slot. Only editable fields: a mark
	// on a field nobody can propose could never fire.
	//
	// A MAP, not a slice, so the template can `index` it. generic-detail.gohtml
	// is parsed by both apps and orbital has no template FuncMap at all — a
	// `has` helper would need adding to two parse sets that are already a
	// documented drift hazard.
	Editable map[string]bool

	// FieldValues maps each row's orbId to its raw editable values as JSON —
	// what a mark compares a proposal against to suppress a no-op. Keyed by
	// orbId rather than row index so the template cannot mis-align them.
	FieldValues map[string]string
}

// MetaRow is one row of the provenance box.
type MetaRow struct {
	// Field is the schema field name, or "" for a row that is not a field (a
	// provenance row). Rows with a Field carry a proposed-change mark slot.
	Field string

	Label string
	Value string

	// IsTimestamp marks a value the browser should render in local time. The
	// decision is the server's because it knows the field's SCHEMA TYPE;
	// sniffing the string shape in the template would guess.
	IsTimestamp bool
}

// GenericRef is one single-valued relationship, rendered as a link.
type GenericRef struct {
	Label string
	Slug  string
	OrbID string
	Name  string
}

// GenericOwned is an owned child rendered inline.
//
// Children handles the WRAPPER case: ClusterBackup has no scalars of its own
// and exists only to hold etcd/velero/s3Sync, so rendering it without its
// grandchildren shows an empty box where the backup configuration should be.
type GenericOwned struct {
	Label  string
	Slug   string
	OrbID  string
	Name   string
	Fields []MetaRow

	// FieldValuesJSON is this CHILD's raw editable values. An edit to an owned
	// child records the child's orbId and never the parent's, so a mark on
	// these rows can only fire from the child's own id.
	FieldValuesJSON string

	Children []GenericOwned
}

// ColumnHeader is one table column: the schema field it reads, and the label
// shown above it.
//
// The LABEL is computed server-side, not in the template. UI.md settles that a
// field label is the title-cased field name, and an integrator building their
// own table from orbital's responses should not have to re-derive that rule — the
// same argument that flattened the export-preview response.
type ColumnHeader struct {
	Field string
	Label string
}

// RefHeader is a link column: a relationship rendered as one cell.
type RefHeader struct {
	Field string
	Label string
	Slug  string
}

// FieldRow is one row of a detail page's field table.
type FieldRow struct {
	Field string
	Label string
	Value any

	// Editable gates the proposed-change mark slot: a mark on a field nobody
	// can propose could never fire.
	Editable bool

	// Blob marks a `jsonString` field — a document, not a value. Rendered
	// pretty-printed in its own scrolling block: as a single line it runs to
	// 600 characters and pushes the table sideways, which is the same problem
	// that kept it out of list columns.
	Blob bool
}

// CreateForm is the New-node form for a list page, resolved server-side.
//
// Generated from the view, never hand-written per type: the fields are the
// type's own editable scalars, the relationships are the ones the SCHEMA marks
// non-null, and the identity comes from the type's `orbIdPattern`. A blank JSON
// editor was the alternative and was rejected — the edit tree works because it
// is prefilled, and empty it offers no affordance and no discoverability.
type CreateForm struct {
	// Kind is the CONCRETE GraphQL type the mutation targets. DGraph generates
	// no add<Interface>, so an interface-backed page cannot create until it
	// knows which implementation is meant.
	Kind  string
	Label string
	DomID string

	// Unavailable, when set, replaces the form with a stated reason and hides
	// the submit — the schema was unreadable, or this type cannot be created.
	Unavailable string

	// NamespaceFrom is the relationship field whose chosen orbId supplies the
	// namespace. Namespace is never typed: it is the prefix of the parent's id,
	// and two sources for one value is two chances to disagree.
	NamespaceFrom string

	// OrbIDTemplate is the type's orbIdPattern with `{kind}` already resolved —
	// `server-{serviceTag}`. The form substitutes the remaining placeholders as
	// they are typed, so the reader sees the identity BEFORE committing to it.
	// Server-side resolution of `{kind}` keeps the kebab/orbIdSuffix rule in one
	// place rather than reimplementing it in JS.
	OrbIDTemplate string

	// OrbIDPaths are the placeholder paths the type's pattern needs, so the
	// form knows which inputs feed the identity preview. Keyed by PATH
	// (`server.serviceTag`), which is what NewOrbID expects.
	OrbIDPaths []string

	Fields    []CreateField
	Relations []CreateRelation

	// Unit are the owned children created in the SAME mutation — the set
	// the editor already edits. Nested, because a nested create is the one
	// nesting DGraph performs atomically.
	Unit []CreateUnitChild
}

// CreateField is one editable scalar on the form.
type CreateField struct {
	Field string
	Label string

	// Type is the GraphQL scalar — Boolean, Int, Int64, Float, DateTime, String.
	// Carried because the mutation sends JSON: a Boolean field given the STRING
	// "false" is rejected by DGraph, and "false" is exactly what a text input
	// produces. It also decides the control: a checkbox beats a box you can type
	// "flase" into.
	Type string

	// Default pre-fills the input. A suggestion, not a hidden write.
	Default string

	// Hint is the placeholder: what this field EXPECTS. Without it a DateTime
	// is an empty box and nobody can tell whether it wants a date, a time or
	// RFC3339 — which is the reason a type would otherwise be hidden from the
	// form with `create: false`.
	Hint string

	// Identity marks a field the orbId is built from: it must be filled before
	// the node has a name at all, so the form marks it required and the preview
	// watches it. Identity fields never carry a default.
	Identity bool
}

// CreateRelation is a relationship the form must supply.
type CreateRelation struct {
	Field    string
	Label    string
	Type     string
	Required bool
	// Options are the existing nodes that may be chosen, as orbId + display
	// name. Resolved server-side: a picker that yields an orbId is the whole
	// job, and typing one from memory is the failure this avoids.
	Options []CreateOption
}

// CreateOption is one pickable existing node.
type CreateOption struct {
	OrbID string
	Name  string
}

// CreateUnitChild is a owned child created alongside its parent.
type CreateUnitChild struct {
	Field string
	Label string
	Kind  string

	// OrbIDTemplate is the CHILD's own orbIdPattern, rewritten against the
	// PARENT's form inputs: `{server.serviceTag}-idrac` becomes
	// `{serviceTag}-idrac`, because the server it refers to is the node being
	// created and its serviceTag is on this very form.
	//
	// Required, not optional: `orbId` is `String! @id`, so a nested child
	// without one fails the whole mutation. Empty means the child's identity
	// cannot be derived from what the form collects, and it is then not offered.
	OrbIDTemplate string

	Fields []CreateField
}
