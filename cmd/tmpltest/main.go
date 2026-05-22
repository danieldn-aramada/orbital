package main

import (
	"bytes"
	"fmt"
	"os"

	orbtemplates "github.com/armada/orbital/web/orb/templates"
	"github.com/armada/orbital/internal/web/data/layout"
)

type statusPageData struct {
	layout.Base
	PageTitle        string
	HasData          bool
	DCName           string
	OCIRegistry      string
	OCIRepo          string
	CurrentVersion   string
	AvailableVersion string
	HasLastImport    bool
}

func main() {
	m := orbtemplates.Map()
	tmpl := m["status"]
	if tmpl == nil {
		fmt.Println("template 'status' not found")
		os.Exit(1)
	}
	for name, t := range m {
		fmt.Printf("template %q: defined templates: ", name)
		for _, tt := range t.Templates() {
			fmt.Printf("%s ", tt.Name())
		}
		fmt.Println()
	}

	data := statusPageData{
		Base: layout.Base{
			Head:        layout.Head{Version: "123"},
			AppVersion:  "v0.0.0-dev",
			BasePath:    "",
			CurrentPath: "/status",
			UI: layout.UIConfig{
				AppName:  "Orb",
				BasePath: "",
				Version:  "123",
				ShowAuth: false,
			},
		},
		PageTitle: "Status",
		HasData:   true,
		DCName:    "colo-galleon",
	}
	var buf bytes.Buffer
	err := tmpl.ExecuteTemplate(&buf, "base.gohtml", data)
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
	fmt.Println("SUCCESS, output length:", buf.Len())
}
