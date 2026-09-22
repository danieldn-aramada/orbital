package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

type UserRow struct {
	ID        int
	Email     string
	Name      string
	Role      string
	CreatedAt string // pre-formatted: "2006-01-02"

	// ProviderOwned marks a user whose role is derived from an identity
	// provider's group claim at every login (Spike 26 mode B). The role control
	// is read-only for these, because an edit here would be silently reverted at
	// their next login — which is the complaint Grafana's skip_org_role_sync
	// exists to answer. RoleSource names the provider for the tooltip.
	ProviderOwned bool
	// RoleSource is the full issuer URL, shown in the tooltip.
	RoleSource string
	// RoleSourceLabel is the issuer's host, shown inline. "from identity
	// provider" is true but useless when an operator is asking WHICH one —
	// orbital names the provider rather than making them hover.
	RoleSourceLabel string
}

type Users struct {
	layout.Base
	PageTitle string
	Users     []UserRow
}
