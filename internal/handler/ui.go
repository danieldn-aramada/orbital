package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/user"
	appversion "github.com/armada/orbital/internal/version"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/armada/orbital/internal/web/data/page"
	webtemplates "github.com/armada/orbital/web/templates/orbital"
	"github.com/labstack/echo/v4"
)

type UI struct {
	dev               bool
	ratelURL          string
	issueTrackerURL   string
	oidcEnabled       bool
	deviceCodeEnabled bool
	backupEnabled    bool
	backupCronSpec   string
	s3Endpoint       string
	s3Bucket         string
	ociConfigured    bool
	ociRegistry      string
	ociRepo          string
	exportDir        string
	schemaPath       string
	restoreAvailable bool
	dgraphURL        string
	version          string
	basePath         string
	db               *ent.Client
	templates        map[string]*template.Template
}

func NewUI(dev bool, ratelURL, issueTrackerURL string, oidcEnabled, deviceCodeEnabled, backupEnabled bool, s3Endpoint, s3Bucket string, basePath string, db *ent.Client) *UI {
	return &UI{
		dev:               dev,
		ratelURL:          ratelURL,
		issueTrackerURL:   issueTrackerURL,
		oidcEnabled:       oidcEnabled,
		deviceCodeEnabled: deviceCodeEnabled,
		backupEnabled:    backupEnabled,
		s3Endpoint:       s3Endpoint,
		s3Bucket:         s3Bucket,
		basePath:         basePath,
		db:               db,
		version:          fmt.Sprintf("%d", time.Now().Unix()),
		templates:        webtemplates.Map(),
	}
}

func (h *UI) SetRestoreAvailable(available bool) {
	h.restoreAvailable = available
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

func (h *UI) SetDGraphURL(url string) {
	h.dgraphURL = url
}

func (h *UI) SetBackupCronSpec(spec string) {
	h.backupCronSpec = spec
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

	var userRole string
	var canMutate bool
	var adminEmails []string
	if h.db != nil && isAuthn && userID > 0 {
		if u, err := h.db.User.Get(c.Request().Context(), userID); err == nil {
			userRole = string(u.Role)
			canMutate = RoleAtLeast(u.Role, user.RoleDev)
		}
		if admins, err := h.db.User.Query().Where(user.RoleEQ(user.RoleAdmin)).All(c.Request().Context()); err == nil {
			adminEmails = make([]string, len(admins))
			for i, a := range admins {
				adminEmails[i] = a.Email
			}
		}
	}

	return layout.Base{
		Head:              layout.Head{Version: version},
		NavBar:            layout.NavBar{RatelURL: h.ratelURL, IssueTrackerURL: h.issueTrackerURL},
		IsAuthn:           isAuthn,
		OIDCEnabled:       h.oidcEnabled,
		DeviceCodeEnabled: h.deviceCodeEnabled,
		User:              layout.User{Id: userID, Name: userName, Email: userEmail, Role: userRole},
		CanMutate:         canMutate,
		AdminEmails:       adminEmails,
		CsrfToken:         csrfToken,
		AppVersion:        appversion.Version,
		BasePath:          h.basePath,
		CurrentPath:       c.Request().URL.Path,
		UI: layout.UIConfig{
			AppName:     "Orbital",
			BasePath:    h.basePath,
			Version:     version,
			ShowAuth:    true,
			APIDocPath:  h.basePath + "/swagger/index.html",
			GraphQLPath: "/graphql",
			MoreLinks: []layout.NavItem{
				{Label: "GitHub", URL: "https://github.com/danieldn-aramada/demo"},
				{Label: "Report Issue"},
			},
			MenuSections: h.buildMenuSections(c.Request().URL.Path, userRole),
		},
	}
}

func (h *UI) buildMenuSections(path, userRole string) []layout.MenuSection {
	bp := h.basePath
	sections := []layout.MenuSection{
		{
			Title: "Config Items",
			Icon:  "fa-solid fa-diagram-project",
			Color: "has-text-primary",
			Items: []layout.MenuItem{
				{Label: "Inventory", Href: bp + "/", Active: path == bp+"/" || path == bp+"/inventory"},
				{Label: "Data Centers", Href: bp + "/datacenters", Active: path == bp+"/datacenters"},
				{Label: "Servers", Href: bp + "/servers", Active: path == bp+"/servers"},
				{Label: "Schema Version", Href: bp + "/schema", Active: path == bp+"/schema"},
			},
		},
		{
			Title: "Edge",
			Icon:  "fa-solid fa-tower-broadcast",
			Color: "has-text-warning",
			Items: []layout.MenuItem{
				{Label: "Export Subgraph", Href: bp + "/export", Active: path == bp+"/export"},
				{Label: "Signed Artifacts", Href: bp + "/signed-artifacts", Active: path == bp+"/signed-artifacts"},
				{Label: "Divergence Reports", Href: bp + "/divergence-reports", Active: path == bp+"/divergence-reports"},
			},
		},
	}

	opsItems := []layout.MenuItem{
		{Label: "Audit Log", Href: bp + "/audit-log", Active: path == bp+"/audit-log"},
		{Label: "Backup Graph", Href: bp + "/backups", Active: path == bp+"/backups"},
		{Label: "Restore Graph", Href: bp + "/restore", Active: path == bp+"/restore"},
	}
	if userRole == "admin" {
		opsItems = append(opsItems, layout.MenuItem{
			Label:  "Users",
			Href:   bp + "/users",
			Active: path == bp+"/users",
		})
	}
	sections = append(sections, layout.MenuSection{
		Title: "Operations",
		Icon:  "fa-solid fa-clock-rotate-left",
		Color: "has-text-danger",
		Items: opsItems,
	})

	return sections
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
	p := page.Backups{
		Base:          h.base(c),
		PageTitle:     "Backups",
		BackupEnabled: h.backupEnabled,
		S3Endpoint:    h.s3Endpoint,
		S3Bucket:      h.s3Bucket,
		ScheduleSpec:  h.backupCronSpec,
	}
	if h.backupCronSpec != "" {
		if parsed, err := cronParser.Parse(h.backupCronSpec); err == nil {
			p.NextRunApprox = parsed.Next(time.Now().UTC()).UTC().Format("2006-01-02 15:04 UTC")
		}
	}
	return h.render(c, "backups", p)
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
	p := page.Export{
		Base:          h.base(c),
		PageTitle:     "Export Subgraph",
		OCIConfigured: h.ociConfigured,
		OCIRegistry:   h.ociRegistry,
		OCIRepo:       h.ociRepo,
		ExportDir:     h.exportDir,
	}
	if h.dgraphURL != "" {
		body, _ := json.Marshal(map[string]string{"query": "{ queryDataCenter { orbId name } }"})
		resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
		if err == nil {
			defer resp.Body.Close()
			var result struct {
				Data struct {
					QueryDataCenter []struct {
						OrbID string `json:"orbId"`
						Name  string `json:"name"`
					} `json:"queryDataCenter"`
				} `json:"data"`
			}
			if json.NewDecoder(resp.Body).Decode(&result) == nil {
				p.DataCenters = make([]page.DataCenterOption, 0, len(result.Data.QueryDataCenter))
				for _, dc := range result.Data.QueryDataCenter {
					p.DataCenters = append(p.DataCenters, page.DataCenterOption{
						OrbID: dc.OrbID,
						Name:  dc.Name,
					})
				}
			}
		}
	}
	return h.render(c, "export", p)
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

func (h *UI) Restore(c echo.Context) error {
	p := page.Restore{
		Base:          h.base(c),
		PageTitle:     "Restore Graph",
		BackupEnabled: h.backupEnabled,
		K8sAvailable:  h.restoreAvailable,
	}
	if sv, err := readSchemaVersion(h.schemaPath); err == nil {
		p.CurrentSchemaVersion = sv
	}
	if h.db != nil && h.backupEnabled {
		backups, err := h.db.Backup.Query().
			Where(backup.StatusEQ(backup.StatusCompleted)).
			Order(backup.ByCompletedAt(sql.OrderDesc())).
			All(c.Request().Context())
		if err == nil {
			p.CompletedBackups = make([]page.BackupOption, 0, len(backups))
			for _, b := range backups {
				label := path.Base(b.S3Key)
				if b.CompletedAt != nil {
					label += " (" + b.CompletedAt.UTC().Format("2006-01-02 15:04 UTC") + ")"
				}
				p.CompletedBackups = append(p.CompletedBackups, page.BackupOption{
					ID:            b.ID.String(),
					Label:         label,
					SchemaVersion: b.SchemaVersion,
				})
			}
		}
	}
	return h.render(c, "restore", p)
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

func (h *UI) Users(c echo.Context) error {
	base := h.base(c)
	var rows []page.UserRow
	if h.db != nil {
		users, err := h.db.User.Query().Order(user.ByCreatedAt(sql.OrderAsc())).All(c.Request().Context())
		if err != nil {
			return fmt.Errorf("query users: %w", err)
		}
		rows = make([]page.UserRow, len(users))
		for i, u := range users {
			rows[i] = page.UserRow{
				ID:        u.ID,
				Email:     u.Email,
				Name:      u.Name,
				Role:      string(u.Role),
				CreatedAt: u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			}
		}
	}
	return h.render(c, "users", page.Users{
		Base:      base,
		PageTitle: "Users",
		Users:     rows,
	})
}

// formatDuration converts a duration to a concise human-readable string.
// e.g. 24h → "24h", 168h → "7d", 30m → "30m".
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	days := int(d.Hours()) / 24
	if days > 0 && d == time.Duration(days)*24*time.Hour {
		return fmt.Sprintf("%dd", days)
	}
	hours := int(d.Hours())
	if hours > 0 && d == time.Duration(hours)*time.Hour {
		return fmt.Sprintf("%dh", hours)
	}
	minutes := int(d.Minutes())
	if minutes > 0 && d == time.Duration(minutes)*time.Minute {
		return fmt.Sprintf("%dm", minutes)
	}
	return d.String()
}
