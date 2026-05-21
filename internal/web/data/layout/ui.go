package layout

// UIConfig carries app-level UI configuration threaded into every page.
// It drives per-binary behaviour (orbital vs orb) without needing separate template sets
// for minor differences.
type UIConfig struct {
	// AppName is the application name shown in the browser and navbar.
	// "Orbital" for the orbital binary; the data center slug (e.g. "colo-galleon") for the orb binary.
	AppName string

	// BasePath is the URL prefix for all routes (e.g. "" or "/orbital").
	BasePath string

	// Version is a cache-busting string appended to static asset URLs.
	Version string

	// EditMode controls how field edits are labelled and routed.
	//   "intent"   (orbital): edits update authoritative design intent via GraphQL mutation.
	//              Save button label: "Save".
	//   "override" (orb): edits are local overrides tracked against imported intent.
	//              Save button label: "Override". Original intent value is preserved.
	EditMode string

	// MoreLinks is the list of items rendered in the "More" dropdown in the navbar.
	// Nil/empty = no More dropdown rendered (used by orb).
	MoreLinks []NavItem

	// ShowAuth controls whether the login/logout section appears in the navbar.
	// true for orbital (auth required); false for orb (no auth in Spike 17).
	ShowAuth bool

	// APIDocPath is the href for the "API" link in the navbar.
	// "/swagger/index.html" for orbital; "/graphql" for orb.
	APIDocPath string
}

// NavItem describes a single entry rendered in a navbar dropdown (MoreLinks).
type NavItem struct {
	Label  string // Display text
	URL    string // Absolute URL (including BasePath)
	Icon   string // Font Awesome class, e.g. "fa-solid fa-server"
	Active bool   // true when this item matches the current route
	IsTodo bool   // renders with .todo class → shows displayTodoToast() on click
}
