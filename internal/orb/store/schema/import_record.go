package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ImportRecord is one entry in orb's import history. Same tag may appear
// multiple times (re-import), so id is a surrogate UUID rather than tag.
type ImportRecord struct {
	ent.Schema
}

func (ImportRecord) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("tag"),
		field.String("digest"),
		field.String("dc_orb_id").Optional(),
		field.String("export_job_id").Optional(),
		field.Time("imported_at").Default(time.Now),
		// Propagation anchors for the config-artifact round-trip (orbital→ACR→Zot→orb).
		// build_at = artifact build/publish time (OCI `org.opencontainers.image.created`);
		// zot_push_at = when the edge Zot mirrored it (search-ext PushTimestamp);
		// dispatched_at = when orb finished consumer dispatch. All optional: absent for
		// courier/upload imports (no Zot landing) or registries without the search extension.
		field.Time("build_at").Optional().Nillable(),
		field.Time("zot_push_at").Optional().Nillable(),
		field.Time("dispatched_at").Optional().Nillable(),
		field.Enum("status").Values("done", "partial", "failed"),
		field.Enum("verification").
			Values("verified", "unverified", "not-applicable").
			Optional(),
		field.Text("layers_json").Optional(),
		field.Text("error").Optional(),
		// Distinguishes operator-triggered imports from those triggered by
		// the auto-import poller (ORB_AUTO_IMPORT_ENABLED). Default "manual"
		// preserves legacy rows written before the flag existed.
		field.Enum("initiated_by").Values("manual", "auto").Default("manual"),
	}
}

func (ImportRecord) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("dc_orb_id"),
		index.Fields("imported_at"),
	}
}

func (ImportRecord) Edges() []ent.Edge {
	return nil
}
