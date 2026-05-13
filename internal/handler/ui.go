package handler

import (
	"crypto/sha256"
	"fmt"
	"html/template"
	"os"
	"time"

	appversion "github.com/armada/orbital/internal/version"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/armada/orbital/internal/web/data/page"
	webtemplates "github.com/armada/orbital/web/templates"
	"github.com/labstack/echo/v4"
)

type UI struct {
	dev             bool
	ratelURL        string
	issueTrackerURL string
	oidcEnabled     bool
	backupEnabled   bool
	s3Endpoint      string
	s3Bucket        string
	ociConfigured   bool
	ociRegistry     string
	ociRepo         string
	exportDir       string
	schemaPath      string
	version         string
	basePath        string
	templates       map[string]*template.Template
}

func NewUI(dev bool, ratelURL, issueTrackerURL string, oidcEnabled, backupEnabled bool, s3Endpoint, s3Bucket string, basePath string) *UI {
	return &UI{
		dev:             dev,
		ratelURL:        ratelURL,
		issueTrackerURL: issueTrackerURL,
		oidcEnabled:     oidcEnabled,
		backupEnabled:   backupEnabled,
		s3Endpoint:      s3Endpoint,
		s3Bucket:        s3Bucket,
		basePath:        basePath,
		version:         fmt.Sprintf("%d", time.Now().Unix()),
		templates:       webtemplates.Map(),
	}
}

// SetOCIConfig passes OCI config to the UI handler for rendering state-aware pages.
func (h *UI) SetOCIConfig(configured bool, registry, repo string) {
	h.ociConfigured = configured
	h.ociRegistry = registry
	h.ociRepo = repo
}

func (h *UI) SetExportDir(dir string) {
	h.exportDir = dir
}

func (h *UI) SetSchemaPath(path string) {
	h.schemaPath = path
}

func (h *UI) render(c echo.Context, name string, data any) error {
	tmpl, ok := h.templates[name]
	if h.dev {
		tmpl, ok = webtemplates.Map()[name]
	}
	if !ok {
		return echo.ErrNotFound
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(c.Response().Writer, "base.gohtml", data)
}

func (h *UI) base(c echo.Context) layout.Base {
	isAuthn, _ := c.Get("is_authn").(bool)
	userID, _ := c.Get("user_id").(int)
	userName, _ := c.Get("user_name").(string)
	userEmail, _ := c.Get("user_email").(string)
	csrfToken, _ := c.Get("csrf_token").(string)
	version := h.version
	if h.dev {
		version = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return layout.Base{
		Head:        layout.Head{Version: version},
		NavBar:      layout.NavBar{RatelURL: h.ratelURL, IssueTrackerURL: h.issueTrackerURL},
		IsAuthn:     isAuthn,
		OIDCEnabled: h.oidcEnabled,
		User:        layout.User{Id: userID, Name: userName, Email: userEmail},
		CsrfToken:   csrfToken,
		AppVersion:  appversion.Version,
		BasePath:    h.basePath,
		CurrentPath: c.Request().URL.Path,
	}
}

func (h *UI) Index(c echo.Context) error {
	return h.render(c, "home", page.Home{
		Base:      h.base(c),
		PageTitle: "Orbital",
	})
}

func (h *UI) DataCenters(c echo.Context) error {
	return h.render(c, "datacenters", page.Home{
		Base:      h.base(c),
		PageTitle: "Data Centers",
	})
}

func (h *UI) Backups(c echo.Context) error {
	return h.render(c, "backups", page.Backups{
		Base:          h.base(c),
		PageTitle:     "Backups",
		BackupEnabled: h.backupEnabled,
		S3Endpoint:    h.s3Endpoint,
		S3Bucket:      h.s3Bucket,
	})
}

func (h *UI) DivergenceReports(c echo.Context) error {
	return h.render(c, "divergence-reports", page.DivergenceReports{
		Base:      h.base(c),
		PageTitle: "Divergence Reports",
	})
}

func (h *UI) AuditLog(c echo.Context) error {
	return h.render(c, "audit-log", page.AuditLog{
		Base:      h.base(c),
		PageTitle: "Audit Log",
	})
}

func (h *UI) Export(c echo.Context) error {
	return h.render(c, "export", page.Export{
		Base:          h.base(c),
		PageTitle:     "Export Subgraph",
		OCIConfigured: h.ociConfigured,
		OCIRegistry:   h.ociRegistry,
		OCIRepo:       h.ociRepo,
		ExportDir:     h.exportDir,
	})
}

func (h *UI) EdgeDelivery(c echo.Context) error {
	return h.render(c, "signed-artifacts", page.EdgeDelivery{
		Base:          h.base(c),
		PageTitle:     "Signed Artifacts",
		OCIConfigured: h.ociConfigured,
		OCIRegistry:   h.ociRegistry,
		OCIRepo:       h.ociRepo,
	})
}

func (h *UI) Servers(c echo.Context) error {
	return h.render(c, "servers", page.Servers{
		Base:      h.base(c),
		PageTitle: "Servers",
	})
}

func (h *UI) Schema(c echo.Context) error {
	content, err := os.ReadFile(h.schemaPath)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	sum := sha256.Sum256(content)
	return h.render(c, "schema", page.Schema{
		Base:      h.base(c),
		PageTitle: "Schema",
		Version:   "v1",
		Checksum:  fmt.Sprintf("%x", sum[:6]),
		SDL:       string(content),
	})
}
