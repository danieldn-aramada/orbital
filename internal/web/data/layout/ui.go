package layout

// UIConfig carries app-level UI configuration threaded into every page.
type UIConfig struct {
	AppName  string
	BasePath string
	Version  string
	// Tagline is rendered below the brand in the navbar when non-empty.
	Tagline string
	// MoreLinks is the list of items rendered in the "More" dropdown in the navbar.
	MoreLinks []NavItem
	ShowAuth   bool
	APIDocPath string
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
}
