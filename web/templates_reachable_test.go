package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAllTemplatesReachable guards a specific regression class: a .gohtml file
// that no parse set references. Such a file is invisible — it compiles, it
// ships (web/embed.go embeds templates/shared and templates/orb wholesale), and
// it rots. Seven of them had accumulated by 2026-09-23, including two
// near-identical copies of a delete modal that both declared id="delete-modal",
// the id orbital.js uses for the live backups delete flow. Adding either to a
// parse set would have silently bound that flow to the wrong modal.
//
// The invariant: every .gohtml under web/templates must have its path appear as
// a string literal in some .go file. Both parse-set spellings count —
// "web/templates/..." (orbital, CWD-relative from the repo root) and
// "templates/..." (orb, relative to the embed.FS root).
//
// This is deliberately strict rather than clever. Resolving reachability
// through {{template "name"}} chains would be more permissive and much easier
// to fool; requiring an explicit path in Go source means a template is reachable
// only if something actually loads it. Verified 2026-09-23: all 61 live
// templates satisfy it, so no template depends on the looser rule.
func TestAllTemplatesReachable(t *testing.T) {
	var templates []string
	err := filepath.WalkDir("templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".gohtml" {
			templates = append(templates, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("no .gohtml files found under web/templates — did the tree move?")
	}

	goSources := readGoSources(t, "..")
	if len(goSources) == 0 {
		t.Fatal("no .go files found — did the repo layout change?")
	}

	for _, tmpl := range templates {
		orbSpelling := `"` + tmpl + `"`         // templates/...
		orbitalSpelling := `"web/` + tmpl + `"` // web/templates/...
		referenced := false
		for _, src := range goSources {
			if strings.Contains(src, orbSpelling) || strings.Contains(src, orbitalSpelling) {
				referenced = true
				break
			}
		}
		if !referenced {
			t.Errorf(
				"web/%s is not referenced by any parse set — it is dead.\n"+
					"Either delete it, or add its path to the parse set that should load it\n"+
					"(web/templates/orbital/templates.go, web/templates/orb/templates.go, or a handler).\n"+
					"Expected to find %s or %s in some .go file.",
				tmpl, orbitalSpelling, orbSpelling,
			)
		}
	}
}

// readGoSources returns the contents of every .go file under root, skipping
// directories that cannot contain parse sets.
func readGoSources(t *testing.T, root string) []string {
	t.Helper()
	skip := map[string]bool{".git": true, "node_modules": true, "static": true, ".local": true}
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk go sources: %v", err)
	}
	return out
}

// TestNoNativeTitleTooltips enforces UI.md's tooltip rule in the only way that
// survives: mechanically.
//
// Native title="" is browser-controlled — ~1s delay, small system font, no
// theming, no positioning control. The rule ("new code MUST NOT add title=")
// was documented and unenforced, so 47 of them accumulated across 15 templates
// and two JS modules, and each new one was copied from a neighbour.
//
// The house replacement is the `.tooltip` class + `data-text` (web/sass/main.scss),
// with `.tooltip.is-wide` for anything longer than a short label.
//
// `title` on <iframe>/<svg>/<abbr> is an accessibility affordance rather than a
// hover tooltip; none exist today, and if one is added it should be allowlisted
// here deliberately rather than by loosening the check.
func TestNoNativeTitleTooltips(t *testing.T) {
	files, err := filepath.Glob("templates/*/*/*.gohtml")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	more, _ := filepath.Glob("templates/*/*/*/*.gohtml")
	files = append(files, more...)

	jsFiles := []string{"shared/static/orbital.js", "shared/static/shared.js", "shared/static/orb.js", "shared/static/configitem-editor.js"}
	files = append(files, jsFiles...)

	if len(files) < 10 {
		t.Fatalf("found only %d files to scan — the layout moved", len(files))
	}

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // a JS file may legitimately not exist in a trimmed tree
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, `title="`) {
				t.Errorf(
					"web/%s:%d uses a native title=\"\" tooltip.\n"+
						"  Use the house tooltip instead: add the `tooltip` class (plus `is-wide`\n"+
						"  for more than a short label) and move the text to data-text=\"…\".\n"+
						"  %s",
					f, i+1, strings.TrimSpace(line),
				)
			}
		}
	}
}
