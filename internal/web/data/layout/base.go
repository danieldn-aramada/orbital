package layout

type Base struct {
	Head
	NavBar
	LoginModal
	RegisterModal
	Footer

	Domain     string // default localhost:8080, production console.com
	Links      []string
	IsAuthn    bool
	OIDCEnabled bool
	CsrfToken  string
	AppVersion string
	BasePath   string

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
}
