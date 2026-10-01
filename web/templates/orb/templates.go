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
	"templates/shared/components/login-gate.gohtml",
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
		"status":    parsePage(fsys, "status", page("templates/orb/pages/status.gohtml")),
		"import":    parsePage(fsys, "import", page("templates/orb/pages/import.gohtml")),
		"inventory": parsePage(fsys, "inventory", page("templates/orb/pages/inventory.gohtml")),
		"schema":    parsePage(fsys, "schema", page("templates/orb/pages/schema.gohtml")),
		// Orb's OWN single-data-centre page — distinct from the shared DataCenter
		// detail, which moved to the generic renderer.
		"datacenter": parsePage(fsys, "datacenter", page("templates/orb/pages/datacenter.gohtml")),
		// The SAME two templates orbital uses. Orb renders them read-only —
		// the editor is gated on CanMutate, which orb never sets.
		"generic-list": parsePage(fsys, "generic-list", page("templates/shared/pages/generic-list.gohtml")),
		// edit-modal is parsed even though orb NEVER renders it — orb sets no
		// can_mutate, so that branch cannot be taken.
		//
		// It is still required: html/template runs contextual escape analysis
		// over the WHOLE template tree at first execution, and that analysis has
		// to resolve every {{template}} reference whether or not the branch is
		// reachable. text/template would not care. Verified by removing it:
		//   html/template:generic-detail.gohtml:32:13: no such template "edit-modal.gohtml"
		//
		// The alternative — a second, modal-free copy of generic-detail for orb
		// — is worse: two templates for one page is how the two apps drift, and
		// that duplication is what this renderer exists to remove.
		"generic-detail": parsePage(fsys, "generic-detail", append(
			page("templates/shared/pages/generic-detail.gohtml"),
			"templates/shared/components/edit-modal.gohtml")),
		"divergence":      parsePage(fsys, "divergence", page("templates/orb/pages/divergence.gohtml")),
		"import-history":  parsePage(fsys, "import-history", page("templates/orb/pages/import-history.gohtml")),
		"publish-history": parsePage(fsys, "publish-history", page("templates/orb/pages/publish-history.gohtml")),

		// No standalone fragment entries remain: DataCenter, cluster and Server
		// all render through the shared GenericRenderer now, which uses the
		// ordinary page templates and their HX-Request fragment block.
	}
}

// ParseFragment parses a partial template file plus any companion templates it
// references via {{template "name" .}}. Used in dev mode for hot reload.
// fsys must be rooted at the web/ directory (use os.DirFS("web") for dev).
// Paths are relative to fsys (e.g. "templates/shared/pages/servers.gohtml").
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
