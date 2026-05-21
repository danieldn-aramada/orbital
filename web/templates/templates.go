package templates

import (
	"html/template"
)

// base is included in every page parse set.
var base = []string{
	"web/shared/templates/layouts/base.gohtml",
	"web/shared/templates/layouts/head.gohtml",
	"web/shared/templates/layouts/footer.gohtml",
	"web/shared/templates/components/navbar.gohtml",
	"web/orbital/templates/components/menu.gohtml",
	"web/shared/templates/components/todo-toast.gohtml",
	"web/orbital/templates/components/report-issue-modal.gohtml",
	"web/orbital/templates/components/login-modal.gohtml",
	"web/shared/templates/components/hint-banner.gohtml",
}

func page(path string) []string {
	files := make([]string, len(base)+1)
	copy(files, base)
	files[len(base)] = path
	return files
}

// LoginForm returns a parsed template for the login form fragment.
// Used by the login handler to re-render the form with error states.
func LoginForm() *template.Template {
	return template.Must(template.ParseFiles("web/orbital/templates/partials/login-form.gohtml"))
}

// Map builds the full template map at startup. Each entry is an isolated
// parse set — base layout/components plus one page — so {{define "page"}}
// is unambiguous per route.
func Map() map[string]*template.Template {
	return map[string]*template.Template{
		"home":               template.Must(template.ParseFiles(page("web/orbital/templates/pages/home.gohtml")...)),
		"datacenters":        template.Must(template.ParseFiles(page("web/orbital/templates/pages/datacenters.gohtml")...)),
		"backups":            template.Must(template.ParseFiles(page("web/orbital/templates/pages/backups.gohtml")...)),
		"divergence-reports": template.Must(template.ParseFiles(page("web/orbital/templates/pages/divergence-reports.gohtml")...)),
		"audit-log":          template.Must(template.ParseFiles(page("web/orbital/templates/pages/audit-log.gohtml")...)),
		"schema":             template.Must(template.ParseFiles(page("web/orbital/templates/pages/schema.gohtml")...)),
		"export":             template.Must(template.ParseFiles(page("web/orbital/templates/pages/export.gohtml")...)),
		"signed-artifacts":   template.Must(template.ParseFiles(page("web/orbital/templates/pages/signed-artifacts.gohtml")...)),
		"servers":            template.Must(template.ParseFiles(page("web/orbital/templates/pages/servers.gohtml")...)),
		"restore":            template.Must(template.ParseFiles(page("web/orbital/templates/pages/restore.gohtml")...)),
	}
}
