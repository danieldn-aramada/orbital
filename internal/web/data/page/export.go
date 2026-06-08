package page

import "github.com/armada/orbital/internal/web/data/layout"

type DataCenterOption struct {
	OrbID string
	Name  string
}

type Export struct {
	layout.Base
	PageTitle     string
	OCIConfigured bool
	OCIRegistry   string
	OCIRepo       string
	ExportDir     string
	DataCenters   []DataCenterOption
}
