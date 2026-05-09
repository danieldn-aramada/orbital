package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

type EdgeDelivery struct {
	layout.Base
	PageTitle    string
	OCIConfigured bool
	OCIRegistry  string
	OCIRepo      string
}
