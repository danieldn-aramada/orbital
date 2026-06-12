package page

import "github.com/armada/orbital/internal/web/data/layout"

type DivergenceRow struct {
	ID            string
	DCOrbID       string
	EntryOrbID    string
	Field         string
	IntendedValue string // pretty-printed JSON value
	OverrideValue string
	Who           string
	FirstSeenAt   string
	LastSeenAt    string

	// Resolution status — empty Action means un-resolved (pending).
	ResolutionAction string // "accept" | "force" | "ignore" | ""
	ResolutionActor  string
	DecidedAt        string
	CbConsumed       bool
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
}

type DivergenceReports struct {
	layout.Base
	PageTitle  string
	Groups     []DivergenceGroup
	CanResolve bool
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
