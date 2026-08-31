package schema

import "github.com/google/uuid"

// newUUIDv7 is the default for UUID primary keys on tables added from 2026-08-31.
//
// v7 rather than v4 because v7 embeds a millisecond timestamp in its high bits,
// so generated ids sort by creation time. Two consequences that matter for a
// database key: inserts append at the right edge of the B-tree instead of
// scattering across it, which is what keeps the index compact; and `ORDER BY id`
// is a sane creation-order fallback rather than noise. RFC 9562 (2024) names v7
// as the choice for new database keys, and v4 for keys that must not leak a
// creation time — these are internal ids on rows that already store created_at,
// so there is nothing to leak.
//
// Wrapped because uuid.NewV7 returns an error (it can fail if the entropy
// source does) while ent's Default wants a plain constructor. Falling back to v4
// rather than panicking: a non-sortable id is a performance characteristic, an
// aborted write is an outage, and both are still valid UUIDs in the same column.
//
// The older tables still default to uuid.New (v4). Mixing versions in one column
// is fine — nothing reads the version — so they migrate whenever the sweep in
// docs/planning/debt.md is picked up, with no rewrite of existing rows.
func newUUIDv7() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}
