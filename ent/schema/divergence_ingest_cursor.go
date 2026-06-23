package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// DivergenceIngestCursor tracks, per data center, the `publishedAt` of the
// most recent divergence report orbital has successfully ingested. The
// ingester's poll loop reads this before deciding whether to apply the
// latest S3 report; equal-or-older publishedAt is skipped as already seen.
//
// Persisted (not in-memory) so the cursor survives pod restarts — a redeploy
// must NOT cause orbital to re-ingest a report it had already processed,
// because re-ingest fires the supersede branch when resolutions exist and
// silently drops operator decisions.
type DivergenceIngestCursor struct {
	ent.Schema
}

func (DivergenceIngestCursor) Fields() []ent.Field {
	return []ent.Field{
		// dc_orb_id is the natural primary key — one cursor per DC.
		field.String("dc_orb_id").NotEmpty().Unique(),

		// last_published_at is the `publishedAt` of the most recent report
		// that successfully applied. Reports with publishedAt <= this value
		// are skipped on poll.
		field.Time("last_published_at"),
	}
}

func (DivergenceIngestCursor) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (DivergenceIngestCursor) Edges() []ent.Edge {
	return nil
}
