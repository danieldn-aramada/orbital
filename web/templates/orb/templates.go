package orbtemplates

import (
	"fmt"
	"html/template"
	"io/fs"
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
// Paths are relative to the web/ directory root (matching embed.FS and os.DirFS("web")).
var base = []string{
	"templates/shared/layouts/base.gohtml",
	"templates/shared/layouts/head.gohtml",
	"templates/shared/layouts/footer.gohtml",
	"templates/shared/components/navbar.gohtml",
	"templates/shared/components/menu.gohtml",
	"templates/shared/components/todo-toast.gohtml",
	"templates/shared/components/hint-banner.gohtml",
	// Stub definitions required by navbar.gohtml references; orb has no auth UI.
	"templates/orb/components/login-modal.gohtml",
	"templates/orb/components/report-issue-modal.gohtml",
	"templates/orb/components/config-item-delete-modal.gohtml",
}

func page(path string) []string {
	files := make([]string, len(base)+1)
	copy(files, base)
	files[len(base)] = path
	return files
}

func parsePage(fsys fs.FS, name string, files []string) *template.Template {
	return template.Must(template.New(name).Funcs(funcMap).ParseFS(fsys, files...))
}

// Map builds the full orb template map. fsys must be rooted at the web/ directory
// (either the embedded web.FS or os.DirFS("web") for dev hot-reload).
func Map(fsys fs.FS) map[string]*template.Template {
	return map[string]*template.Template{
		"status":         parsePage(fsys, "status", page("templates/orb/pages/status.gohtml")),
		"import":         parsePage(fsys, "import", page("templates/orb/pages/import.gohtml")),
		"inventory":      parsePage(fsys, "inventory", page("templates/orb/pages/inventory.gohtml")),
		"schema":         parsePage(fsys, "schema", page("templates/orb/pages/schema.gohtml")),
		"datacenter":     parsePage(fsys, "datacenter", page("templates/orb/pages/datacenter.gohtml")),
		"servers":        parsePage(fsys, "servers", page("templates/orb/pages/servers.gohtml")),
		"divergence":     parsePage(fsys, "divergence", page("templates/orb/pages/divergence.gohtml")),
		"import-history": parsePage(fsys, "import-history", page("templates/orb/pages/import-history.gohtml")),

		// Standalone fragments — rendered directly (no base layout).
		// Base name must equal the file basename so tmpl.Execute picks up the
		// parsed file content (see ParseFragment comment below).
		"datacenter-tab": template.Must(template.New("datacenter-tab.gohtml").Funcs(funcMap).ParseFS(fsys,
			"templates/shared/partials/datacenter-tab.gohtml",
			"templates/shared/components/edit-modal-datacenter.gohtml",
		)),
		"server-tab": template.Must(template.New("server-tab.gohtml").Funcs(funcMap).ParseFS(fsys,
			"templates/shared/partials/server-tab.gohtml",
			"templates/shared/components/edit-modal-server.gohtml",
		)),
	}
}

// ParseFragment parses a partial template file plus any companion templates it
// references via {{template "name" .}}. Used in dev mode for hot reload.
// fsys must be rooted at the web/ directory (use os.DirFS("web") for dev).
// Paths are relative to fsys (e.g. "templates/shared/partials/datacenter-tab.gohtml").
//
// The base template name MUST equal the file basename of `path` — otherwise
// tmpl.Execute(w, data) runs an empty base template and returns "incomplete
// or empty template". Naming the base to match the file makes Execute pick up
// the parsed content (Go's html/template merges them).
func ParseFragment(fsys fs.FS, path string, companions ...string) (*template.Template, error) {
	files := append([]string{path}, companions...)
	t, err := template.New(filepath.Base(path)).Funcs(funcMap).ParseFS(fsys, files...)
	if err != nil {
		return nil, fmt.Errorf("parse fragment %s: %w", path, err)
	}
	return t, nil
}
