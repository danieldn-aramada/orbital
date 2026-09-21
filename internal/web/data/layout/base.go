package layout

type Base struct {
	Head
	NavBar
	LoginModal
	RegisterModal
	Footer

	UI UIConfig

	Domain      string // default localhost:8080, production console.com
	Links       []string
	IsAuthn     bool
	OIDCEnabled bool
	// LoginError carries a human-readable reason when a sign-in was refused and
	// the user was redirected back. Without it a refusal is a silent bounce to
	// the home page, which reads as "the button does nothing" — the failure mode
	// this exists to prevent.
	LoginError     string
	LoginErrorCode string
	CsrfToken      string
	AppVersion     string
	BasePath       string
	CurrentPath    string
	CanMutate      bool
	AdminEmails    []string
	// PendingDivergences is the count of divergence entries with no operator
	// resolution yet. Rendered as a badge on the menu so edge drift is visible
	// without navigating to /divergence-reports — divergence is a notification,
	// not a destination. 0 renders nothing.
	PendingDivergences int

	User
}

type Head struct {
	Description   string
	LinksJsText   []string
	LinksJsModule []string
	LinksCss      []string
	Version       string
}

type NavBar struct {
	RatelURL        string
	IssueTrackerURL string
}

type LoginModal struct {
	LoginUrl  string
	CsrfToken string
}

type RegisterModal struct {
	RegisterUrl string
}

type Footer struct {
}

type User struct {
	Id    int
	Name  string
	Email string
	Role  string
}
