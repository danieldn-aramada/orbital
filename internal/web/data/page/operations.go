package page

import "github.com/armada/orbital/internal/web/data/layout"

type DivergenceReports struct {
	layout.Base
	PageTitle string
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
	AppliedAt string
	AppliedBy string
	SDL       string
}
