package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ApprovalRequest is one proposed change awaiting review — the generic
// maker-checker record. It is deliberately action-agnostic: the engine owns the
// lifecycle (open → approved → merged, plus rejected/closed) and knows nothing
// about what is being changed. A new action type adds rows with a different
// `action_type` and a different `payload` shape plus its own adapter — and
// requires NO schema change here. See docs/reference/CHANGE-CONTROL.md.
//
// v1 implements exactly one action type: `config.mutation` ("Change Request"),
// whose payload is a store-neutral field-delta changeset and whose execute step
// is the merge to DGraph.
type ApprovalRequest struct {
	ent.Schema
}

func (ApprovalRequest) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),

		// action_type selects the adapter (e.g. "config.mutation"). A plain
		// string, NOT an ent enum: the whole point of the generic engine is that
		// a second action type is additive. An enum would force a schema change
		// (and a migration) for every new one — exactly what §4 rules out.
		field.String("action_type").NotEmpty(),

		field.String("title").NotEmpty(),
		field.Text("description").Optional().Default(""),

		// status is an enum — unlike action_type, the lifecycle IS fixed by the
		// engine and shared by every action type (D8). `merged` is the terminal
		// executed state; it keeps the config flavor's name because that is the
		// word the UI and every operator uses.
		//
		// `approved` is NOT a value here, and its absence is the point. Approvals
		// count only while the hash they were cast against is still current, so
		// "approved" is a function of (valid approvals >= required) evaluated at
		// read time — the same derive-don't-maintain rule staleness follows. A
		// stored `approved` would go wrong the moment a third party touches a
		// covered entity: the column would still say approved while the approvals
		// backing it no longer count. Leaving the value out of the enum makes
		// that state unrepresentable rather than merely discouraged.
		field.Enum("status").
			Values("open", "rejected", "merged", "closed").
			Default("open"),

		// author is the proposer, from actorFromContext. Distinct from the
		// AuditMixin's created_by (which is generic row provenance) because
		// `proposer != approver` enforcement reads this field specifically.
		field.String("author").NotEmpty(),

		// base_hash is the adapter's staleness token, captured once at open and
		// NEVER recomputed (D13). For config.mutation it is a graphdiff content
		// hash over the touched entities and their owned subtrees.
		//
		// There is deliberately no `stale` column: staleness is derived on every
		// read by re-hashing the current graph and comparing. A stored boolean is
		// precisely the copy that drifts — see D13.
		field.String("base_hash").NotEmpty(),

		// base_present is the set of orbIds that EXISTED when base_hash was
		// captured. Without it, an absent target at merge time is ambiguous: it
		// is either a normal create (absent at open, absent at merge) or a
		// deleted target (present at open, absent at merge) that must hard-fail
		// with 409 TARGET_MISSING rather than be silently recreated from a
		// partial field-delta. See D13's table.
		field.JSON("base_present", []string{}).Optional(),

		// payload is the action-type-specific body, opaque to the engine. For
		// config.mutation: {"namespace": ..., "changes": [{orbId, type, op, set,
		// clear}, ...]}. Filters like ?orbId= and ?namespace= are served from
		// this jsonb (D13) rather than a denormalised projection table that would
		// need re-syncing on every PATCH.
		field.JSON("payload", json.RawMessage{}),

		// executed_at / executed_by record the merge. Generic names because the
		// engine's step is "execute"; config.mutation calls it merge.
		field.Time("executed_at").Optional().Nillable(),
		field.String("executed_by").Optional().Default(""),
	}
}

func (ApprovalRequest) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (ApprovalRequest) Indexes() []ent.Index {
	return []ent.Index{
		// The queue view: open/approved requests, newest first.
		index.Fields("status", "created_at"),
		// "mine" filter.
		index.Fields("author"),
		index.Fields("action_type"),
		// GIN on the payload so "which requests touch this orbId" is an indexed
		// containment lookup rather than a scan-plus-render. Without it the
		// pending-change badge — which fires on every detail view — would load
		// every request and pay DGraph to derive staleness for each, only to
		// discard nearly all of them. D13 held the denormalised change_target
		// table in reserve for this; the index is the cheaper first step it
		// named, and it costs no re-sync on PATCH.
		index.Fields("payload").
			Annotations(entsql.IndexTypes(map[string]string{dialect.Postgres: "GIN"})),
	}
}

func (ApprovalRequest) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("approvals", Approval.Type),
		edge.To("merge_attempts", MergeAttempt.Type),
	}
}
