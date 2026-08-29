package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

// The Change Control pages are deliberately thin structs: the shell is
// server-rendered (menu, title, role gate) and every row, diff and action comes
// from the public API client-side. Orbital's UI is a consumer of that API, not
// a privileged path with its own query — so there is no data to carry here.

// ChangeRequests is the review queue.
type ChangeRequests struct {
	layout.Base
	PageTitle string
}

// ChangeRequestDetail is one request's review view. ID is the only thing the
// server knows; everything rendered comes from GET /api/v1/change-requests/:id.
type ChangeRequestDetail struct {
	layout.Base
	PageTitle string
	ID        string
}

// ApprovalPolicies is the admin page for protected classes.
type ApprovalPolicies struct {
	layout.Base
	PageTitle string
}
