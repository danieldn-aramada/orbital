// Package approval holds the store-neutral types of orbital's maker-checker
// engine: the payload an approval request carries and the result a merge
// records. They are deliberately free of both ent and DGraph — ent stores them
// as opaque jsonb, and the DGraph dialect is produced at merge time from these
// shapes, never stored.
package approval

// ActionTypeConfigMutation is v1's only action type: a proposed set of
// ConfigItem field changes. New action types are additive — a different
// action_type string with a different payload shape and its own adapter, no
// schema change to the engine tables.
const ActionTypeConfigMutation = "config.mutation"

// Status values as the API reports them. Only Open, Rejected, Merged and
// Closed are ever STORED; Approved is derived from the valid-approval count at
// read time and can therefore revert to Open on its own when the base moves.
const (
	StatusOpen     = "open"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusMerged   = "merged"
	StatusClosed   = "closed"
)

// Op is what a change item does to its target.
//
// Explicit rather than inferred from whether the entity exists: inference makes
// the same request mean different things depending on when it runs, which is
// exactly the ambiguity approval is supposed to remove. A reviewer approving
// "update" must not have it silently become "create" because someone deleted
// the entity mid-review.
type Op string

const (
	// OpUpsert writes the target end-state, creating the entity if absent.
	OpUpsert Op = "upsert"
	// OpUpdate writes the target end-state and requires the entity to exist.
	OpUpdate Op = "update"
	// OpDelete removes the entity. Deleting an absent entity is a no-op success.
	OpDelete Op = "delete"
)

// ChangeItem is one entity's target end-state — a field delta, not a replay of
// the mutation that produced it (D2). Storing desired state means the change
// stays reviewable and re-appliable no matter what happened to the graph in
// between; a mutation log would have to be replayed against a base that moved.
type ChangeItem struct {
	// OrbID identifies the target. Globally unique across every type, which is
	// why Type can be resolved from it rather than supplied.
	OrbID string `json:"orbId"`

	// Type is the ConfigItem type (e.g. "Server"). Optional on input — orbital
	// resolves it from OrbID — and always present on output. Required only when
	// the entity does not exist yet, since there is then nothing to resolve from.
	Type string `json:"type,omitempty"`

	Op Op `json:"op"`

	// Set is the fields to write, as a field-name → value map matching the
	// GraphQL schema.
	//
	// FLAT — one item per entity, never a tree. An edge value may carry only an
	// identity key (orbId/id); anything else is REJECTED at creation by
	// validateFields, because DGraph LINKS on an edge rather than writing
	// through it — a nested payload returns success and silently discards the
	// child's field values. An owned child therefore gets its own entry in
	// Changes under its own orbId; nothing is split at merge.
	//
	// Flatness is load-bearing, not stylistic: the field-level conflict guard
	// keys on orbId → predicate → value, so a nested item would need conflicts
	// located by path and BaseValues to become a tree diff.
	Set map[string]any `json:"set,omitempty"`

	// Clear is the fields to unset. Separate from Set because a GraphQL `set`
	// of null is a no-op in DGraph — clearing requires a `remove`.
	Clear []string `json:"clear,omitempty"`
	// IfVersion is the entity's `version` as the caller read it. Supplying it
	// makes the item conditional at ENTITY level: orbital refuses at creation if
	// the entity has moved since, and refuses at merge if it moves between
	// review and apply.
	//
	// It means exactly what `ifVersion` means on /graphql, deliberately — a
	// client integrating against both APIs should not meet two different
	// concurrency concepts for the same question.
	//
	// POINTER, because absent and 0 are different answers. 0 is a version a
	// caller could legitimately have read, so a value type would silently turn
	// "I did not check" into "I read version 0".
	//
	// ONE per item, never per field: an entity has one version, so it has one
	// precondition. That is also the second reason two items on one orbId are
	// refused — the first would bump the entity, making the second's
	// precondition stale before it ran.
	//
	// Omitted is legal and means "unconditional at entity level"; the scope
	// anchor (base_hash) still guards the merge. Supplying it against an entity
	// that does not exist is REFUSED at validation rather than ignored: there is
	// no version to match, and a caller that asked for a check and silently did
	// not get one is worse off than one that never asked.
	IfVersion *int `json:"ifVersion,omitempty"`
}

// Changeset is the config.mutation payload. Single-namespace by construction:
// the base snapshot, the approval policy, and the reviewer's mental model are
// all namespace-scoped, and a cross-namespace request would have no single
// policy governing it.
type Changeset struct {
	Namespace string       `json:"namespace"`
	Changes   []ChangeItem `json:"changes"`
}

// ItemResult is one item's outcome within a merge attempt, recorded in the
// order applied. A merge applies items one at a time, so a partial application
// is a real outcome that has to be representable — see MergeAttempt.
type ItemResult struct {
	OrbID   string `json:"orbId"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}
