package layout

// PageActions declares which mutation controls are available on a given page.
// Templates check these flags to conditionally render buttons and tabs.
// Orbital sets all applicable flags true; orb is read-only and only sets Reload.
type PageActions struct {
	Create           bool // "New…" buttons on list pages
	Edit             bool // edit existing records via JSON modal
	Delete           bool // delete records
	Reload           bool // reload / refresh data
	ShowAuditTab     bool // Audit Log tab on DC and server detail pages
	ShowDivergenceTab bool // Divergence Reports tab on DC detail page (orbital-side reports)
}

// OrbitalActions is the default PageActions for all orbital pages.
var OrbitalActions = PageActions{
	Create:            true,
	Edit:              true,
	Delete:            true,
	Reload:            true,
	ShowAuditTab:      true,
	ShowDivergenceTab: true,
}

// OrbActions is the default PageActions for all orb pages (read-only).
var OrbActions = PageActions{
	Reload: true,
}
