package page

import "github.com/armada/orbital/internal/web/data/layout"

type DivergenceRow struct {
	ID            string
	DCOrbID       string
	EntryOrbID    string
	TypeName      string // orbital GraphQL type name (e.g. "IdracSettings"); displayed above orbId
	Field         string
	IntendedValue string // pretty-printed JSON value
	OverrideValue string
	Who           string
	FirstSeenAt   string
	LastSeenAt    string

	// Resolution status — empty Action means un-resolved (pending).
	ResolutionAction string // "accept" | "reject" | "ignore" | ""
	ResolutionActor  string
	DecidedAt        string
	// Stale is true when orbital's current DGraph version for the target
	// ConfigItem differs from the row's `intended_at_version` captured at
	// ingest. Means intent has moved since the report was made — the
	// observation is no longer authoritative. UI shows a "stale" badge and
	// allows dismissal. Only computed when intended_at_version is non-nil;
	// nil-version rows can't be checked this way and stay Stale=false.
	Stale bool
}

type DivergenceGroup struct {
	DCOrbID    string
	LastSeenAt string
	Total      int
	Pending    int
	Forced     int
	Accepted   int
	Ignored    int
	Rows       []DivergenceRow

	// AlreadyPublished is true when there is a completed RegistryArtifact
	// for this DC whose completed_at is later than the most recent resolution's
	// decided_at — meaning the current set of resolutions has already been
	// published. The row's per-DC Publish button is rendered in a permanent
	// "Published" disabled state to enforce one-shot publish semantics
	// (no other operator can re-trigger; republish goes through /export).
	AlreadyPublished bool
	// PublishedTag is the tag of the latest completed publish for this DC,
	// surfaced in the disabled button's label (e.g. "Published v17"). Empty
	// when AlreadyPublished is false.
	PublishedTag string

	// IgnoreOnly is true when every entry in the group has been decided AND
	// every decision is Ignore. Ignore doesn't drive edge actuation — the
	// publish-now button is irrelevant for these rows and hidden in the UI.
	// True only when Total > 0 (an empty group is degenerate, not "ignore-only").
	IgnoreOnly bool
}

type DivergenceReports struct {
	layout.Base
	PageTitle      string
	Groups         []DivergenceGroup
	CanResolve     bool
	BackupEnabled  bool // true when ORBITAL_S3_BUCKET et al are set
	S3Endpoint     string
	S3Bucket       string
}

type AuditLog struct {
	layout.Base
	PageTitle string
}

type Schema struct {
	layout.Base
	PageTitle string
	Version   string
	Checksum  string
	SDL       string
}
