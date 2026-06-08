package orbtemplates

import (
	"fmt"
	"html/template"
	"path/filepath"
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
	"web/templates/shared/layouts/base.gohtml",
	"web/templates/shared/layouts/head.gohtml",
	"web/templates/shared/layouts/footer.gohtml",
	"web/templates/shared/components/navbar.gohtml",
	"web/templates/shared/components/menu.gohtml",
	"web/templates/shared/components/todo-toast.gohtml",
	"web/templates/shared/components/hint-banner.gohtml",
	// Stub definitions required by navbar.gohtml references; orb has no auth UI.
	"web/templates/orb/components/login-modal.gohtml",
	"web/templates/orb/components/report-issue-modal.gohtml",
	"web/templates/orb/components/config-item-delete-modal.gohtml",
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
		"status":         parsePage("status", page("web/templates/orb/pages/status.gohtml")),
		"import":         parsePage("import", page("web/templates/orb/pages/import.gohtml")),
		"inventory":      parsePage("inventory", page("web/templates/orb/pages/inventory.gohtml")),
		"schema":         parsePage("schema", page("web/templates/orb/pages/schema.gohtml")),
		"datacenter":     parsePage("datacenter", page("web/templates/orb/pages/datacenter.gohtml")),
		"servers":        parsePage("servers", page("web/templates/orb/pages/servers.gohtml")),
		"divergence":     parsePage("divergence", page("web/templates/orb/pages/divergence.gohtml")),
		"import-history": parsePage("import-history", page("web/templates/orb/pages/import-history.gohtml")),

		// Standalone fragments — rendered directly (no base layout).
		// Base name must equal the file basename so tmpl.Execute picks up the
		// parsed file content (see ParseFragment comment below).
		"datacenter-tab": template.Must(template.New("datacenter-tab.gohtml").Funcs(funcMap).ParseFiles(
			"web/templates/shared/partials/datacenter-tab.gohtml",
			"web/templates/shared/components/edit-modal-datacenter.gohtml",
		)),
		"server-tab": template.Must(template.New("server-tab.gohtml").Funcs(funcMap).ParseFiles(
			"web/templates/shared/partials/server-tab.gohtml",
			"web/templates/shared/components/edit-modal-server.gohtml",
		)),
	}
}

// ParseFragment parses a partial template file plus any companion templates it
// references via {{template "name" .}}. Used in dev mode for hot reload.
// Variadic companions matches the prod registration sets above.
//
// The base template name MUST equal the file basename of `path` — otherwise
// tmpl.Execute(w, data) runs an empty base template and returns "incomplete
// or empty template". Naming the base to match the file makes Execute pick up
// the parsed content (Go's html/template merges them).
func ParseFragment(path string, companions ...string) (*template.Template, error) {
	files := append([]string{path}, companions...)
	t, err := template.New(filepath.Base(path)).Funcs(funcMap).ParseFiles(files...)
	if err != nil {
		return nil, fmt.Errorf("parse fragment %s: %w", path, err)
	}
	return t, nil
}
