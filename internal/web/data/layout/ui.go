package layout

// AuditPanelDefaultLimit is the row cap requested by in-page audit tabs
// (DC / Server / Cluster detail panels). The list handler accepts up to 500;
// this is the UI's chosen default and the single source of truth shared by
// the Go templates and the JS that fetches the panel.
const AuditPanelDefaultLimit = 200

// UIConfig carries app-level UI configuration threaded into every page.
type UIConfig struct {
	AppName  string
	BasePath string
	Version  string
	// Tagline is rendered below the brand in the navbar when non-empty.
	// Each slice entry is one line.
	Tagline []string
	// MoreLinks is the list of items rendered in the "More" dropdown in the navbar.
	MoreLinks  []NavItem
	ShowAuth   bool
	APIDocPath string
	// GraphQLPath is the navbar link target for the GraphQL endpoint.
	// Both orbital and orb use "/graphql" — GraphQL is not URL-versioned,
	// per convention (GitHub, GitLab, NetBox, Apollo). See CLAUDE.md.
	GraphQLPath string
	// AuditPanelLimit is rendered into window.ORBITAL_CONFIG and read by
	// shared.js when fetching in-page audit tabs. Defaults to
	// AuditPanelDefaultLimit; override per-page only if a panel genuinely
	// needs a different cap.
	AuditPanelLimit int
	// MenuSections drives the sidebar menu. Handler builds this from the current path.
	MenuSections []MenuSection
}

// NavItem describes a single entry rendered in a navbar dropdown (MoreLinks).
type NavItem struct {
	Label  string
	URL    string
	Icon   string
	Active bool
	IsTodo bool
}

// MenuSection is a group of related links in the sidebar menu.
type MenuSection struct {
	Title string
	Icon  string // Font Awesome class e.g. "fa-solid fa-diagram-project"
	Color string // Bulma color class e.g. "has-text-primary"
	Items []MenuItem
}

// MenuItem is a single link in a MenuSection.
type MenuItem struct {
	Label  string
	Href   string
	IsTodo bool
	Active bool
	// Badge renders a count chip after the label. Zero renders nothing. Used to
	// surface work waiting on the operator (unreviewed divergences) so the item
	// is a notification rather than a destination they must remember to visit.
	Badge int
	// BadgeSrc makes the chip ASYNCHRONOUS: the menu renders an empty span and
	// JS fills it from this API path. Use it when the count is expensive to
	// compute, since the menu is on EVERY page — a badge that costs a scan per
	// page load makes the whole app slower to surface a number nobody is
	// waiting on. The count read is `total` from the response, so the badge and
	// the page it links to can never disagree.
	//
	// Mutually exclusive with Badge. Path is BasePath-relative and prefixed by
	// the JS, per the data-* convention.
	BadgeSrc string
}
