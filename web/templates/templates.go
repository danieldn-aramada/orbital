package templates

import (
	"html/template"
)

// base is included in every page parse set.
var base = []string{
	"web/templates/layouts/base.gohtml",
	"web/templates/layouts/head.gohtml",
	"web/templates/layouts/footer.gohtml",
	"web/templates/components/navbar.gohtml",
	"web/templates/components/menu.gohtml",
	"web/templates/components/todo-toast.gohtml",
	"web/templates/components/report-issue-modal.gohtml",
	"web/templates/components/login-modal.gohtml",
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
	return template.Must(template.ParseFiles("web/templates/fragments/login-form.gohtml"))
}

// Map builds the full template map at startup. Each entry is an isolated
// parse set — base layout/components plus one page — so {{define "page"}}
// is unambiguous per route.
func Map() map[string]*template.Template {
	return map[string]*template.Template{
		"home":               template.Must(template.ParseFiles(page("web/templates/pages/home.gohtml")...)),
		"datacenters":        template.Must(template.ParseFiles(page("web/templates/pages/datacenters.gohtml")...)),
		"backups":            template.Must(template.ParseFiles(page("web/templates/pages/backups.gohtml")...)),
		"divergence-reports": template.Must(template.ParseFiles(page("web/templates/pages/divergence-reports.gohtml")...)),
		"audit-log":          template.Must(template.ParseFiles(page("web/templates/pages/audit-log.gohtml")...)),
		"schema":             template.Must(template.ParseFiles(page("web/templates/pages/schema.gohtml")...)),
		"export":             template.Must(template.ParseFiles(page("web/templates/pages/export.gohtml")...)),
		"signed-artifacts": template.Must(template.ParseFiles(page("web/templates/pages/signed-artifacts.gohtml")...)),
		"servers":        template.Must(template.ParseFiles(page("web/templates/pages/servers.gohtml")...)),
		"restore":        template.Must(template.ParseFiles(page("web/templates/pages/restore.gohtml")...)),
	}
}
