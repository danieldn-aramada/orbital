package templates

import (
	"html/template"
	"io/fs"
)

// base is included in every page parse set.
var base = []string{
	"templates/shared/layouts/base.gohtml",
	"templates/shared/layouts/head.gohtml",
	"templates/shared/layouts/footer.gohtml",
	"templates/shared/components/navbar.gohtml",
	"templates/shared/components/menu.gohtml",
	"templates/shared/components/login-gate.gohtml",
	"templates/orbital/components/report-issue-modal.gohtml",
	"templates/orbital/components/login-modal.gohtml",
	"templates/orbital/partials/access-required.gohtml",
	"templates/orbital/components/config-item-delete-modal.gohtml",
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
func LoginForm(fsys fs.FS) *template.Template {
	return template.Must(template.ParseFS(fsys, "templates/orbital/partials/login-form.gohtml"))
}

// Map builds the full template map at startup. Each entry is an isolated
// parse set — base layout/components plus one page — so {{define "page"}}
// is unambiguous per route.
func Map(fsys fs.FS) map[string]*template.Template {
	return map[string]*template.Template{
		"home":               template.Must(template.ParseFS(fsys, page("templates/orbital/pages/home.gohtml")...)),
		"backups":            template.Must(template.ParseFS(fsys, page("templates/orbital/pages/backups.gohtml")...)),
		"divergence-reports": template.Must(template.ParseFS(fsys, page("templates/orbital/pages/divergence-reports.gohtml")...)),
		"audit-log":          template.Must(template.ParseFS(fsys, page("templates/orbital/pages/audit-log.gohtml")...)),
		"schema":             template.Must(template.ParseFS(fsys, page("templates/orbital/pages/schema.gohtml")...)),
		"export":             template.Must(template.ParseFS(fsys, page("templates/orbital/pages/export.gohtml")...)),
		"publish-history": template.Must(template.ParseFS(fsys, page(
			"templates/orbital/pages/publish-history.gohtml",
			"templates/orbital/partials/publish-history-tabs.gohtml")...)),
		"publish-history-compare": template.Must(template.ParseFS(fsys, page(
			"templates/orbital/pages/publish-history-compare.gohtml",
			"templates/orbital/partials/publish-history-tabs.gohtml")...)),
		// One template serves EVERY ConfigItem type, so a new type needs no entry
		// here. These two are the whole generic renderer.
		"generic-list": template.Must(template.ParseFS(fsys, page("templates/shared/pages/generic-list.gohtml")...)),
		"generic-detail": template.Must(template.ParseFS(fsys, page(
			"templates/shared/pages/generic-detail.gohtml",
			// The SAME edit modal the bespoke pages parse — one template, not a
			// generic copy of one.
			"templates/shared/components/edit-modal.gohtml")...)),
		"restore": template.Must(template.ParseFS(fsys, page("templates/orbital/pages/restore.gohtml")...)),
		"users":   template.Must(template.ParseFS(fsys, page("templates/orbital/pages/users.gohtml")...)),

		"change-requests":       template.Must(template.ParseFS(fsys, page("templates/orbital/pages/change-requests.gohtml")...)),
		"change-request-detail": template.Must(template.ParseFS(fsys, page("templates/orbital/pages/change-request-detail.gohtml")...)),
		"approval-policies":     template.Must(template.ParseFS(fsys, page("templates/orbital/pages/approval-policies.gohtml")...)),
	}
}
