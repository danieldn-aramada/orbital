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
}

func page(path string) []string {
	files := make([]string, len(base)+1)
	copy(files, base)
	files[len(base)] = path
	return files
}

// Map builds the full template map at startup. Each entry is an isolated
// parse set — base layout/components plus one page — so {{define "page"}}
// is unambiguous per route.
func Map() map[string]*template.Template {
	return map[string]*template.Template{
		"home": template.Must(template.ParseFiles(page("web/templates/pages/home.gohtml")...)),
	}
}
