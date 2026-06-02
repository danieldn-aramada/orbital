package orbtemplates

import (
	"fmt"
	"html/template"
	"strings"
)

var funcMap = template.FuncMap{
	"add": func(a, b int) int { return a + b },
	// mediaTypeLabel extracts a short human-readable name from a vendor media type.
	// "application/vnd.armada.configbundle.manifest.v1+yaml" → "configbundle"
	// "application/vnd.orbital.subgraph.data.v1+gzip"        → "subgraph"
	"mediaTypeLabel": func(mediaType string) string {
		s := strings.TrimPrefix(mediaType, "application/vnd.")
		if s == mediaType {
			parts := strings.SplitN(mediaType, "/", 2)
			return parts[len(parts)-1]
		}
		parts := strings.SplitN(s, ".", 3)
		if len(parts) >= 2 {
			return parts[1]
		}
		return s
	},
}

// base lists the shared + orb-specific layout files included in every page parse set.
var base = []string{
	"web/shared/templates/layouts/base.gohtml",
	"web/shared/templates/layouts/head.gohtml",
	"web/shared/templates/layouts/footer.gohtml",
	"web/shared/templates/components/navbar.gohtml",
	"web/shared/templates/components/menu.gohtml",
	"web/shared/templates/components/todo-toast.gohtml",
	"web/shared/templates/components/hint-banner.gohtml",
	// Stub definitions required by navbar.gohtml references; orb has no auth UI.
	"web/orb/templates/components/login-modal.gohtml",
	"web/orb/templates/components/report-issue-modal.gohtml",
}

func page(path string) []string {
	files := make([]string, len(base)+1)
	copy(files, base)
	files[len(base)] = path
	return files
}

func parsePage(name string, files []string) *template.Template {
	return template.Must(template.New(name).Funcs(funcMap).ParseFiles(files...))
}

// Map builds the full orb template map at startup. Each entry is an isolated
// parse set — base layout + components + one page.
func Map() map[string]*template.Template {
	return map[string]*template.Template{
		"status":         parsePage("status", page("web/orb/templates/pages/status.gohtml")),
		"import":         parsePage("import", page("web/orb/templates/pages/import.gohtml")),
		"inventory":      parsePage("inventory", page("web/orb/templates/pages/inventory.gohtml")),
		"schema":         parsePage("schema", page("web/orb/templates/pages/schema.gohtml")),
		"datacenter":     parsePage("datacenter", page("web/orb/templates/pages/datacenter.gohtml")),
		"servers":        parsePage("servers", page("web/orb/templates/pages/servers.gohtml")),
		"divergence":     parsePage("divergence", page("web/orb/templates/pages/divergence.gohtml")),
		"import-history": parsePage("import-history", page("web/orb/templates/pages/import-history.gohtml")),

		// Standalone fragments — rendered directly (no base layout).
		"datacenter-tab": template.Must(template.New("datacenter-tab").Funcs(funcMap).ParseFiles(
			"web/shared/templates/partials/datacenter-tab.gohtml",
			"web/shared/templates/components/edit-modal-datacenter.gohtml",
		)),
		"server-tab": template.Must(template.New("server-tab").Funcs(funcMap).ParseFiles(
			"web/shared/templates/partials/server-tab.gohtml",
			"web/shared/templates/components/edit-modal-server.gohtml",
		)),
	}
}

// ParseFragment parses a single partial template file. Used in dev mode for hot reload.
func ParseFragment(path string) (*template.Template, error) {
	t, err := template.New("fragment").Funcs(funcMap).ParseFiles(path)
	if err != nil {
		return nil, fmt.Errorf("parse fragment %s: %w", path, err)
	}
	return t, nil
}
