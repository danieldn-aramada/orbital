package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
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
		// A plain auto-increment bigint, not a UUID.
		//
		// The identifier people actually use is `namespace`-`number` (see below);
		// this is the surrogate key behind it. UUID bought nothing here — orbital
		// has one PostgreSQL, no distributed writes, and its own backup/restore
		// covers DGraph, not this database — while every comparable system
		// (GitHub, GitLab, Jira) uses an integer plus a short human id, which is
		// the pattern worth matching.
		field.Int64("id"),

		// namespace and number form the human identifier: `colo-42`.
		//
		// Denormalised from payload.namespace on purpose. It is immutable for the
		// life of the request (a changeset is single-namespace by construction),
		// and every lookup by human id filters on it — reading it out of jsonb on
		// the path that serves `/change-requests/colo-42` would make the primary
		// lookup a containment query.
		//
		// number is per-namespace, so each data center counts from 1 — the Jira
		// PROJ-42 model applied to orbital's natural partition. The unique index
		// below is what makes it safe under concurrent creates.
		field.String("namespace").NotEmpty().Immutable(),
		field.Int("number").Positive().Immutable(),

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

		// base_versions is the version vector base_hash is a fingerprint OF:
		// orbId -> version, over the same scope, captured at the same instant and
		// recomputed on exactly the same occasions (amend, and the rebase after a
		// partial merge).
		//
		// It exists because a fingerprint cannot name the offender. base_hash
		// answers "did anything in scope move" and never "which one", so a stale
		// request refuses wholesale and an operator has to go and find out what
		// changed. With the vector, merge diffs it against the current one and
		// names the entities — for EVERY request, including ones whose author
		// never sent a precondition.
		//
		// Deliberately NOT the client's `ifVersion`. That is the author's read at
		// proposal time, and once anything moves that entity the token is
		// permanently wrong: re-approval — which is how staleness is meant to be
		// cleared, in one click — could never satisfy it, and only an amend
		// could. base_versions moves with the review instead, because it is
		// re-captured wherever base_hash is.
		//
		// Optional: rows written before this field existed decode to nil, and the
		// refusal falls back to the unnamed one they have always produced.
		field.JSON("base_versions", map[string]int{}).Optional(),

		// base_present is the set of orbIds that EXISTED when base_hash was
		// captured. Without it, an absent target at merge time is ambiguous: it
		// is either a normal create (absent at open, absent at merge) or a
		// deleted target (present at open, absent at merge) that must hard-fail
		// with 409 TARGET_MISSING rather than be silently recreated from a
		// partial field-delta. See D13's table.
		field.JSON("base_present", []string{}).Optional(),

		// base_effect is the DELTA this request would apply, computed once against
		// the same snapshot base_hash was captured from, and — like base_hash and
		// base_present — never recomputed.
		//
		// It exists because `payload` states a request's SCOPE, not its effect.
		// The API accepts a complete end-state on purpose so a reconcile-style
		// client can assert one, so a payload naming 22 fields may change 1, and
		// a queue counting payload fields tells a reviewer the wrong number.
		// Every declarative system solves this the same way — accept full state,
		// then make the delta a first-class artifact: Terraform's plan, GitHub's
		// stored diff stat, `kubectl diff`.
		//
		// STORED, not derived, and that is consistent with the derive-don't-
		// maintain rule above rather than an exception to it. That rule governs
		// state which changes ON ITS OWN (staleness, approval validity). A delta
		// is a fact about a moment — exactly like base_hash, which is stored for
		// the same reason. A saved Terraform plan pairs its diff with the state
		// anchor it was computed against, and refuses to apply when state moved;
		// here `stale` is that refusal, already derived.
		//
		// Optional: rows written before this field existed decode to nil and fall
		// back to the payload-derived summary, which is what they always showed.
		field.JSON("base_effect", json.RawMessage{}).Optional(),

		// base_values is the ANCESTOR: orbId -> predicate -> value, for exactly
		// the fields this changeset writes, as they stood when the request was
		// opened. It is what a three-way comparison needs and what base_hash
		// cannot supply — base_hash is a fingerprint of the version vector, so it
		// answers "did anything move" but never "what was it".
		//
		// InfraHub and NetBox get this for free from a materialized branch: the
		// branch point IS the ancestor. Orbital renders branches instead of
		// copying them (orbId is @id, so a branch's copy of an entity cannot
		// coexist with main's), so the ancestor has to be recorded.
		//
		// Stored in graphdiff-normalized, predicate-keyed form — the SAME shape a
		// fresh snapshot produces — so the merge-time comparison is between two
		// values that went through one normalizer. Comparing a stored raw value
		// against a normalized one would disagree on exactly the scalars DGraph
		// round-trips as strings.
		//
		// Optional: absent means "no ancestor recorded", and the guard falls back
		// to the entity-level base_hash, which is what every row predating this
		// field has.
		field.JSON("base_values", map[string]map[string]any{}).Optional(),

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
		// The human identifier. UNIQUE is not decoration: numbers are allocated
		// as max(number)+1 per namespace, and this index is what turns a
		// concurrent double-allocation into a retryable constraint error instead
		// of two requests silently sharing `colo-42`.
		index.Fields("namespace", "number").Unique(),
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
