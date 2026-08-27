package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

type EdgeDelivery struct {
	layout.Base
	PageTitle     string
	OCIConfigured bool
	OCIRegistry   string
	OCIRepo       string
	// ActiveTab drives the Publish History tab bar ("artifacts" | "compare").
	// The tabs are routes, not client-side toggles, so the active one is
	// server-rendered rather than restored from localStorage.
	ActiveTab string
}

// PublishHistoryCompare backs /publish-history/compare. Deliberately carries no
// diff data: the page is a thin renderer over GET /api/v1/export/compare, so an
// upstream client can reproduce this view from the same public API.
type PublishHistoryCompare struct {
	layout.Base
	PageTitle string
	ActiveTab string
}
