package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestImportMapCoversAllModuleImports guards a specific regression class:
// after a deploy, a browser that has an older shared.js cached will download
// the newer orbital.js (cache-busted via ?v= on its <script> tag) but reuse
// the stale shared.js because the relative import `./shared.js` resolves to
// a URL with no cache-buster. If the deploy added exports to shared.js, the
// browser fails with "Module does not provide an export named X" and orbital's
// UI silently breaks.
//
// head.gohtml's <script type="importmap"> block remaps every cross-file
// module URL to a ?v={{.Version}} variant so cache invalidation flows
// through the whole module graph. This test walks web/shared/static/*.js,
// finds every `import ... from './...'` specifier, and asserts each resolved
// URL has a matching key in the import map. Adding a new relative import
// without adding the corresponding map entry fails this test.
//
// If this test fails, the error message tells you the exact line to add.
func TestImportMapCoversAllModuleImports(t *testing.T) {
	head, err := os.ReadFile(filepath.Join("templates", "shared", "layouts", "head.gohtml"))
	if err != nil {
		t.Fatalf("read head.gohtml: %v", err)
	}

	keys := extractImportMapKeys(t, string(head))
	if len(keys) == 0 {
		t.Fatal(`no <script type="importmap"> block found in head.gohtml — did it move?`)
	}

	entries, err := os.ReadDir(filepath.Join("shared", "static"))
	if err != nil {
		t.Fatalf("read shared/static: %v", err)
	}

	// Two ES-module import shapes to catch:
	//   1. `import ... from './foo.js'`   (named/default/namespace, possibly multi-line)
	//   2. `import './foo.js'`             (side-effect only, one-line)
	// The former can span lines; matching `from '…'` anywhere works.
	// The latter has no `from`; match `^import '…'` explicitly.
	fromRe := regexp.MustCompile(`from\s+['"]([^'"]+)['"]`)
	sideEffectRe := regexp.MustCompile(`(?m)^import\s+['"]([^'"]+)['"]`)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		content, err := os.ReadFile(filepath.Join("shared", "static", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		specs := make(map[string]bool)
		for _, m := range fromRe.FindAllStringSubmatch(string(content), -1) {
			specs[m[1]] = true
		}
		for _, m := range sideEffectRe.FindAllStringSubmatch(string(content), -1) {
			specs[m[1]] = true
		}
		for spec := range specs {
			if !strings.HasPrefix(spec, "./") {
				continue // bare specifier or absolute URL — not our scope
			}
			resolved := "/static/" + strings.TrimPrefix(spec, "./")
			if !keys[resolved] {
				t.Errorf(
					"%s imports %q (resolves to %s) — MISSING from import map.\n"+
						"Add this line to the `imports` block in web/templates/shared/layouts/head.gohtml:\n"+
						"  %q: %q,",
					e.Name(), spec, resolved,
					"{{.BasePath}}"+resolved, "{{.BasePath}}"+resolved+"?v={{.Version}}",
				)
			}
		}
	}

	// Also cover inline <script type="module"> imports in head.gohtml itself.
	// Any specifier under {{.BasePath}}/static/... must be in the map for the
	// same reason (they load through the browser's module graph).
	inlineRe := regexp.MustCompile(`from\s+['"]{{\.BasePath}}(/static/[^'"?]+)`)
	for _, m := range inlineRe.FindAllStringSubmatch(string(head), -1) {
		spec := m[1]
		if !keys[spec] {
			t.Errorf(
				"head.gohtml inline module imports %s — MISSING from import map.\n"+
					"Add this line to the `imports` block:\n"+
					"  %q: %q,",
				spec,
				"{{.BasePath}}"+spec, "{{.BasePath}}"+spec+"?v={{.Version}}",
			)
		}
	}
}

// extractImportMapKeys returns the set of import-map keys (with {{.BasePath}}
// stripped) from the head.gohtml source. Returns an empty map if no
// importmap block exists.
func extractImportMapKeys(t *testing.T, html string) map[string]bool {
	t.Helper()
	start := strings.Index(html, `<script type="importmap">`)
	if start < 0 {
		return nil
	}
	end := strings.Index(html[start:], `</script>`)
	if end < 0 {
		t.Fatal(`unterminated <script type="importmap"> block`)
	}
	block := html[start : start+end]

	// Match "KEY": "VALUE" — take the LHS.
	keyRe := regexp.MustCompile(`"([^"]+)"\s*:\s*"[^"]+"`)
	matches := keyRe.FindAllStringSubmatch(block, -1)

	keys := make(map[string]bool, len(matches))
	for _, m := range matches {
		key := strings.ReplaceAll(m[1], "{{.BasePath}}", "")
		keys[key] = true
	}
	return keys
}
