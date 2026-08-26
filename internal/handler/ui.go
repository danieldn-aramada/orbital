package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/divergenceresolution"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/dgraphschema"
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
	backupEnabled     bool
	backupCronSpec    string
	s3Endpoint        string
	s3Bucket          string
	ociConfigured     bool
	ociRegistry       string
	ociRepo           string
	exportDir         string
	schemaPath        string
	restoreAvailable  bool
	dgraphURL         string
	dgraphAdminURL    string
	version           string
	basePath          string
	db                *ent.Client
	logger            *slog.Logger
	templates         map[string]*template.Template
}

func NewUI(dev bool, ratelURL, issueTrackerURL string, oidcEnabled, deviceCodeEnabled, backupEnabled bool, s3Endpoint, s3Bucket string, basePath string, db *ent.Client, logger *slog.Logger) *UI {
	if logger == nil {
		logger = slog.Default()
	}
	return &UI{
		dev:               dev,
		ratelURL:          ratelURL,
		issueTrackerURL:   issueTrackerURL,
		oidcEnabled:       oidcEnabled,
		deviceCodeEnabled: deviceCodeEnabled,
		backupEnabled:     backupEnabled,
		s3Endpoint:        s3Endpoint,
		s3Bucket:          s3Bucket,
		basePath:          basePath,
		db:                db,
		logger:            logger,
		version:           fmt.Sprintf("%d", time.Now().Unix()),
		templates:         webtemplates.Map(),
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

func (h *UI) SetDGraphAdminURL(url string) {
	h.dgraphAdminURL = url
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
	return renderHTML(c, tmpl, "base.gohtml", data)
}

// renderFragment executes a single named {{define}} block within a page's
// parse set. Used for HX-Request swaps where the caller wants just one chunk
// of the page back, not the full layout.
func (h *UI) renderFragment(c echo.Context, page, fragment string, data any) error {
	tmpl, ok := h.templates[page]
	if h.dev {
		tmpl, ok = webtemplates.Map()[page]
	}
	if !ok {
		return echo.ErrNotFound
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return renderHTML(c, tmpl, fragment, data)
}

// pendingDivergenceCount returns the number of divergence entries the operator
// has not yet resolved — the menu badge. "Pending" mirrors the /divergence-reports
// definition exactly: an entry with no DivergenceResolution row on
// (entry_orb_id, field).
//
// Two small queries + a set difference rather than the List handler's per-entry
// lookup, because this runs on EVERY page render. The two tables are 10s–100s of
// rows; if that stops being true, cache it rather than reintroducing N+1.
// Any error yields 0 — a badge must never break a page render.
func (h *UI) pendingDivergenceCount(c echo.Context) int {
	if h.db == nil {
		return 0
	}
	ctx := c.Request().Context()
	entries, err := h.db.DivergenceEntry.Query().
		Select(divergenceentry.FieldEntryOrbID, divergenceentry.FieldField).All(ctx)
	if err != nil || len(entries) == 0 {
		return 0
	}
	resolutions, err := h.db.DivergenceResolution.Query().
		Select(divergenceresolution.FieldEntryOrbID, divergenceresolution.FieldField).All(ctx)
	if err != nil {
		return 0
	}
	resolved := make(map[string]bool, len(resolutions))
	for _, r := range resolutions {
		resolved[r.EntryOrbID+"\x00"+r.Field] = true
	}
	n := 0
	for _, e := range entries {
		if !resolved[e.EntryOrbID+"\x00"+e.Field] {
			n++
		}
	}
	return n
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

	pendingDivergences := h.pendingDivergenceCount(c)

	return layout.Base{
		Head:               layout.Head{Version: version},
		PendingDivergences: pendingDivergences,
		NavBar:             layout.NavBar{RatelURL: h.ratelURL, IssueTrackerURL: h.issueTrackerURL},
		IsAuthn:            isAuthn,
		OIDCEnabled:        h.oidcEnabled,
		DeviceCodeEnabled:  h.deviceCodeEnabled,
		User:               layout.User{Id: userID, Name: userName, Email: userEmail, Role: userRole},
		CanMutate:          canMutate,
		AdminEmails:        adminEmails,
		CsrfToken:          csrfToken,
		AppVersion:         appversion.Version,
		BasePath:           h.basePath,
		CurrentPath:        c.Request().URL.Path,
		UI: layout.UIConfig{
			AppName:         "Orbital",
			Tagline:         []string{"Graph-native source of truth", "for modular data centers"},
			BasePath:        h.basePath,
			Version:         version,
			ShowAuth:        true,
			APIDocPath:      h.basePath + "/swagger/index.html",
			GraphQLPath:     "/graphql",
			AuditPanelLimit: layout.AuditPanelDefaultLimit,
			MoreLinks: []layout.NavItem{
				{Label: "GitHub", URL: "https://github.com/danieldn-aramada/demo"},
				{Label: "Report Issue"},
			},
			MenuSections: h.buildMenuSections(c.Request().URL.Path, userRole, pendingDivergences),
		},
	}
}

func (h *UI) buildMenuSections(path, userRole string, pendingDivergences int) []layout.MenuSection {
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
				{Label: "Clusters", Href: bp + "/clusters", Active: path == bp+"/clusters"},
				{Label: "Network Devices", Href: bp + "/network", Active: path == bp+"/network"},
				{Label: "Schema Version", Href: bp + "/schema", Active: path == bp+"/schema"},
			},
		},
		{
			Title: "Edge",
			Icon:  "fa-solid fa-tower-broadcast",
			Color: "has-text-warning",
			Items: []layout.MenuItem{
				{Label: "Export Subgraph", Href: bp + "/export", Active: path == bp+"/export"},
				{Label: "Publish History", Href: bp + "/publish-history", Active: path == bp+"/publish-history"},
				{Label: "Divergence Reports", Href: bp + "/divergence-reports", Active: path == bp+"/divergence-reports", Badge: pendingDivergences},
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
	base := h.base(c)
	var groups []page.DivergenceGroup
	if h.db != nil {
		ctx := c.Request().Context()
		entries, err := h.db.DivergenceEntry.Query().
			Order(ent.Desc(divergenceentry.FieldLastSeenAt)).
			All(ctx)
		if err != nil {
			return fmt.Errorf("query divergence entries: %w", err)
		}

		idx := map[string]int{}
		// Tracks max(decided_at) per group as raw time.Time — used post-loop
		// to determine whether this group's resolutions have already been
		// covered by a completed publish (one-shot publish semantics from the
		// per-row button on /divergence-reports).
		lastDecidedByGroup := map[int]time.Time{}
		for _, e := range entries {
			row := page.DivergenceRow{
				ID:            e.ID.String(),
				DCOrbID:       e.DcOrbID,
				EntryOrbID:    e.EntryOrbID,
				TypeName:      e.TypeName,
				Field:         e.Field,
				IntendedValue: formatDivergenceValue(e.IntendedValue),
				OverrideValue: formatDivergenceValue(e.OverrideValue),
				Who:           e.Who,
				FirstSeenAt:   e.FirstSeenAt.UTC().Format("2006-01-02 15:04 UTC"),
				LastSeenAt:    e.LastSeenAt.UTC().Format("2006-01-02 15:04 UTC"),
			}
			// Per ADR 012: resolutions in the table are the operator's current
			// decision by construction. The ingester wipes them when orb
			// publishes a content-differing report, so anything still here
			// applies to the current snapshot.
			res, err := h.db.DivergenceResolution.Query().
				Where(
					divergenceresolution.EntryOrbID(e.EntryOrbID),
					divergenceresolution.Field(e.Field),
				).
				Only(ctx)
			if err == nil {
				row.ResolutionAction = string(res.Action)
				row.ResolutionActor = res.Actor
				row.DecidedAt = res.DecidedAt.UTC().Format("2006-01-02 15:04 UTC")
			}

			gi, ok := idx[e.DcOrbID]
			if !ok {
				groups = append(groups, page.DivergenceGroup{
					DCOrbID:    e.DcOrbID,
					LastSeenAt: row.LastSeenAt,
				})
				gi = len(groups) - 1
				idx[e.DcOrbID] = gi
			}
			g := &groups[gi]
			g.Rows = append(g.Rows, row)
			g.Total++
			switch row.ResolutionAction {
			case "reject":
				g.Forced++
			case "accept":
				g.Accepted++
			case "ignore":
				g.Ignored++
			default:
				g.Pending++
			}
			if row.LastSeenAt > g.LastSeenAt {
				g.LastSeenAt = row.LastSeenAt
			}
			if err == nil && res.DecidedAt.After(lastDecidedByGroup[gi]) {
				lastDecidedByGroup[gi] = res.DecidedAt
			}
		}

		// IgnoreOnly: every entry decided, every decision Ignore. Hides the
		// Publish button — Ignore doesn't drive edge actuation, no urgency.
		for gi := range groups {
			g := &groups[gi]
			g.IgnoreOnly = g.Total > 0 && g.Pending == 0 && g.Forced == 0 && g.Accepted == 0
		}

		// AlreadyPublished check: for each group with no pending decisions,
		// look up the most recent completed publish for this DC and compare
		// its completed_at to the group's latest resolution. If the publish
		// came after, the current resolutions are already covered — the row's
		// Publish button stays disabled across sessions, enforcing one-shot
		// publish semantics (republish goes via /export).
		for gi := range groups {
			g := &groups[gi]
			if g.Pending > 0 {
				continue
			}
			lastDecided := lastDecidedByGroup[gi]
			if lastDecided.IsZero() {
				continue
			}
			latest, err := h.db.RegistryArtifact.Query().
				Where(
					registryartifact.DatacenterID(g.DCOrbID),
					registryartifact.StatusEQ(registryartifact.StatusCompleted),
				).
				Order(ent.Desc(registryartifact.FieldCompletedAt)).
				First(ctx)
			if err != nil {
				continue
			}
			if latest.CompletedAt != nil && latest.CompletedAt.After(lastDecided) {
				g.AlreadyPublished = true
				g.PublishedTag = latest.Tag
			}
		}
	}
	data := page.DivergenceReports{
		Base:          base,
		PageTitle:     "Divergence Reports",
		Groups:        groups,
		CanResolve:    RoleAtLeast(user.Role(base.User.Role), user.RoleDev),
		BackupEnabled: h.backupEnabled,
		S3Endpoint:    h.s3Endpoint,
		S3Bucket:      h.s3Bucket,
	}
	// HX-Request callers (the Refresh button) get just the table fragment.
	if c.Request().Header.Get("HX-Request") == "true" {
		return h.renderFragment(c, "divergence-reports", "divergence-content", data)
	}
	return h.render(c, "divergence-reports", data)
}

func formatDivergenceValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "—"
	}
	return string(raw)
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
	return h.render(c, "publish-history", page.EdgeDelivery{
		Base:          h.base(c),
		PageTitle:     "Publish History",
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

func (h *UI) Clusters(c echo.Context) error {
	return h.render(c, "clusters", page.Clusters{
		Base:      h.base(c),
		PageTitle: "Clusters",
	})
}

func (h *UI) NetworkDevices(c echo.Context) error {
	return h.render(c, "network", page.NetworkDevices{
		Base:      h.base(c),
		PageTitle: "Network Devices",
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

// Schema renders the GraphQL schema currently active in DGraph — the honest
// source of truth, not the on-disk file. Version is still read from the
// sibling schema/VERSION file because that label is human-set (bumped manually
// per ADR 007) and isn't stored in DGraph; the file is the right home for it.
// SDL comes from DGraph's `getGQLSchema` admin query, so a fresh / wiped
// DGraph renders an "Awaiting import" state instead of lying about a schema
// the file claims is loaded.
func (h *UI) Schema(c echo.Context) error {
	sdl, err := dgraphschema.Active(c.Request().Context(), h.dgraphAdminURL)
	if err != nil {
		return fmt.Errorf("fetch active schema: %w", err)
	}
	version, err := readSchemaVersion(h.schemaPath)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	sum := sha256.Sum256([]byte(sdl))
	return h.render(c, "schema", page.Schema{
		Base:      h.base(c),
		PageTitle: "Schema",
		Version:   version,
		Checksum:  fmt.Sprintf("%x", sum[:6]),
		SDL:       sdl,
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
