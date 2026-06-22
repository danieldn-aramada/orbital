package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// DivergenceEntry is one current field-level divergence ingested from orb's
// published report in S3. Rows for a DC are scoped to the lifetime of a single
// report ingest: when orbital ingests a content-differing report, all prior
// entries for that DC (and their resolutions) are dropped and replaced with
// the incoming set. See ADR 012.
type DivergenceEntry struct {
	ent.Schema
}

func (DivergenceEntry) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),

		// dc_orb_id identifies the data center this divergence came from.
		// Derived from the S3 prefix orbital polled (e.g. "colo:colo-galleon").
		field.String("dc_orb_id").NotEmpty(),

		// entry_orb_id is the ConfigItem the field belongs to. Often differs
		// from the Server's orbId — e.g. IdracSettings has its own orbId.
		field.String("entry_orb_id").NotEmpty(),

		// field is the DGraph schema field name on entry_orb_id (e.g. "sshEnabled").
		field.String("field").NotEmpty(),

		// type_name is the orbital GraphQL type of entry_orb_id (e.g. "Server",
		// "IdracSettings"). Carried from cb-bundler's mapping layer through orb
		// into the published report. Required for the Accept handler to dispatch
		// the matching `update{Type}` mutation; entries with empty type_name fall
		// back to manual resolution.
		field.String("type_name").Optional().Default(""),

		// intended_value and override_value carried verbatim from the report.
		// JSON-typed because orbital field values can be any DGraph scalar/array.
		field.JSON("intended_value", json.RawMessage{}).Optional(),
		field.JSON("override_value", json.RawMessage{}).Optional(),

		// who set the local override (e.g. "local:admin").
		field.String("who").NotEmpty(),

		// first_seen_at is the report-entry `when` from the first report
		// that included this (dc, orbId, field). Never updated after creation —
		// preserves the true "since when" for the cloud admin.
		field.Time("first_seen_at"),

		// last_seen_at is the report-entry `when` from the most recent
		// report that included this row. Updated on each ingest.
		field.Time("last_seen_at"),

		// last_report_published_at is the publishedAt of the report we
		// loaded this row from. Used for poller idempotency — skip a report
		// whose publishedAt matches what we already ingested.
		field.Time("last_report_published_at"),
	}
}

func (DivergenceEntry) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (DivergenceEntry) Indexes() []ent.Index {
	return []ent.Index{
		// Uniqueness: one row per (DC, orbId, field). UPSERT key for the poller.
		index.Fields("dc_orb_id", "entry_orb_id", "field").Unique(),
		// Per-DC fetch optimization for the UI and poller.
		index.Fields("dc_orb_id", "last_seen_at"),
	}
}

func (DivergenceEntry) Edges() []ent.Edge {
	return nil
}
