// Package page provides shared template loading utilities for orbital and orb.
package page

import (
	"fmt"
	"html/template"
	"io/fs"
	"path/filepath"
)

// LoadTemplates walks the given file system, collects all .gohtml files,
// and parses them into a single template set. All templates are named by
// their base filename (e.g. "base.gohtml", "navbar.gohtml").
//
// Usage:
//
//	// Orbital (production — embed.FS):
//	t, err := LoadTemplates(orbitalFS)
//
//	// Orbital (dev mode — hot reload):
//	t, err := LoadTemplates(os.DirFS("."))
//
//	// Orb:
//	t, err := LoadTemplates(orbFS)
func LoadTemplates(fsys fs.FS) (*template.Template, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".gohtml" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk templates: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no .gohtml files found")
	}
	return template.ParseFS(fsys, paths...)
}
