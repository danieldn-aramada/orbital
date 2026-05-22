package orbtemplates

import (
	"fmt"
	"html/template"
)

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

// Map builds the full orb template map at startup. Each entry is an isolated
// parse set — base layout + components + one page.
func Map() map[string]*template.Template {
	return map[string]*template.Template{
		"status":         template.Must(template.ParseFiles(page("web/orb/templates/pages/status.gohtml")...)),
		"import":         template.Must(template.ParseFiles(page("web/orb/templates/pages/import.gohtml")...)),
		"inventory":      template.Must(template.ParseFiles(page("web/orb/templates/pages/inventory.gohtml")...)),
		"schema":         template.Must(template.ParseFiles(page("web/orb/templates/pages/schema.gohtml")...)),
		"datacenter":     template.Must(template.ParseFiles(page("web/orb/templates/pages/datacenter.gohtml")...)),
		"servers":        template.Must(template.ParseFiles(page("web/orb/templates/pages/servers.gohtml")...)),
		"divergence":     template.Must(template.ParseFiles(page("web/orb/templates/pages/divergence.gohtml")...)),
		"import-history": template.Must(template.ParseFiles(page("web/orb/templates/pages/import-history.gohtml")...)),

		// Standalone fragments — rendered directly (no base layout).
		"datacenter-tab": template.Must(template.ParseFiles(
			"web/shared/templates/partials/datacenter-tab.gohtml",
			"web/shared/templates/components/edit-modal-datacenter.gohtml",
		)),
		"server-tab": template.Must(template.ParseFiles(
			"web/shared/templates/partials/server-tab.gohtml",
			"web/shared/templates/components/edit-modal-server.gohtml",
		)),
	}
}

// ParseFragment parses a single partial template file. Used in dev mode for hot reload.
func ParseFragment(path string) (*template.Template, error) {
	t, err := template.ParseFiles(path)
	if err != nil {
		return nil, fmt.Errorf("parse fragment %s: %w", path, err)
	}
	return t, nil
}
