package templates

import (
	"html/template"
)

// base is included in every page parse set.
var base = []string{
	"web/templates/shared/layouts/base.gohtml",
	"web/templates/shared/layouts/head.gohtml",
	"web/templates/shared/layouts/footer.gohtml",
	"web/templates/shared/components/navbar.gohtml",
	"web/templates/shared/components/menu.gohtml",
	"web/templates/shared/components/todo-toast.gohtml",
	"web/templates/orbital/components/report-issue-modal.gohtml",
	"web/templates/orbital/components/login-modal.gohtml",
	"web/templates/shared/components/hint-banner.gohtml",
	"web/templates/orbital/partials/access-required.gohtml",
	"web/templates/orbital/components/config-item-delete-modal.gohtml",
}

// page builds a parse set: the shared base plus the page file, plus any extra
// partials that page needs. Variadic so a partial shared by a *subset* of pages
// (e.g. the Publish History tab bar, used by two) can be included where it's
// needed instead of being added to `base` and parsed into every page.
func page(paths ...string) []string {
	files := make([]string, 0, len(base)+len(paths))
	files = append(files, base...)
	files = append(files, paths...)
	return files
}

// LoginForm returns a parsed template for the login form fragment.
// Used by the login handler to re-render the form with error states.
func LoginForm() *template.Template {
	return template.Must(template.ParseFiles("web/templates/orbital/partials/login-form.gohtml"))
}

// DeviceCodePage returns a parsed template for the device-code auth page.
// Includes head.gohtml so static assets pick up the same `?v=` cache-busting
// as every other page. Does NOT include base.gohtml or nav components —
// this is an unauthenticated landing rendered without app chrome.
func DeviceCodePage() *template.Template {
	return template.Must(template.ParseFiles(
		"web/templates/shared/layouts/head.gohtml",
		"web/templates/orbital/pages/device-code.gohtml",
	))
}

// Map builds the full template map at startup. Each entry is an isolated
// parse set — base layout/components plus one page — so {{define "page"}}
// is unambiguous per route.
func Map() map[string]*template.Template {
	return map[string]*template.Template{
		"home":               template.Must(template.ParseFiles(page("web/templates/orbital/pages/home.gohtml")...)),
		"datacenters":        template.Must(template.ParseFiles(page("web/templates/orbital/pages/datacenters.gohtml")...)),
		"backups":            template.Must(template.ParseFiles(page("web/templates/orbital/pages/backups.gohtml")...)),
		"divergence-reports": template.Must(template.ParseFiles(page("web/templates/orbital/pages/divergence-reports.gohtml")...)),
		"audit-log":          template.Must(template.ParseFiles(page("web/templates/orbital/pages/audit-log.gohtml")...)),
		"schema":             template.Must(template.ParseFiles(page("web/templates/orbital/pages/schema.gohtml")...)),
		"export":             template.Must(template.ParseFiles(page("web/templates/orbital/pages/export.gohtml")...)),
		"publish-history": template.Must(template.ParseFiles(page(
			"web/templates/orbital/pages/publish-history.gohtml",
			"web/templates/orbital/partials/publish-history-tabs.gohtml")...)),
		"publish-history-compare": template.Must(template.ParseFiles(page(
			"web/templates/orbital/pages/publish-history-compare.gohtml",
			"web/templates/orbital/partials/publish-history-tabs.gohtml")...)),
		"servers":            template.Must(template.ParseFiles(page("web/templates/orbital/pages/servers.gohtml")...)),
		"clusters":           template.Must(template.ParseFiles(page("web/templates/orbital/pages/clusters.gohtml")...)),
		"network":            template.Must(template.ParseFiles(page("web/templates/orbital/pages/network.gohtml")...)),
		"restore":            template.Must(template.ParseFiles(page("web/templates/orbital/pages/restore.gohtml")...)),
		"users":              template.Must(template.ParseFiles(page("web/templates/orbital/pages/users.gohtml")...)),
	}
}
