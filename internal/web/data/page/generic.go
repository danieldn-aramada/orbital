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

	// Facet, when set, is the filter dropdown this page offers.
	Facet *Facet

	// Unavailable, when set, replaces the table with a stated reason. An empty
	// table would say "there are none of these", which is a different claim
	// from "orbital could not look".
	Unavailable string
}

// Facet is a list page's filter dropdown, resolved server-side.
//
// Both the column position and the option list are computed here rather than in
// JavaScript. Orbital's UI is a consumer of orbital's API like any other, so
// anything it needs to draw this control an integrator needs too — and "walk
// the rendered rows collecting distinct values" is precisely the kind of client
// re-implementation the export-preview flattening was about.
type Facet struct {
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
	Rows       []map[string]any
}

// Views is the view-list settings page. It carries no rows: the table is filled
// from GET /api/v1/views by JS, keeping orbital's UI a consumer of its own API.
type Views struct {
	layout.Base
	PageTitle string
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
// own table from /api/v1/views should not have to re-derive that rule — the
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
