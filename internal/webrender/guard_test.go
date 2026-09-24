package webrender

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoDirectTemplateExecuteIntoResponse guards the buffer-then-write rule at
// the only level that holds: the source.
//
// The rule ("all HTML rendering goes through RenderHTML") was documented in
// UI.md and enforced nowhere. Orb's four render sites violated it from the day
// they were written — not by oversight, but because the helper was unexported
// in internal/handler and internal/orbserver structurally could not call it.
// Ten orb pages and every orb HTMX fragment could silently truncate: a template
// referencing a field its render struct lacks committed a 200 with a partial
// body, no 500 and nothing in the log.
//
// A prose rule cannot catch that. This can: executing a template directly into
// c.Response() commits bytes before html/template can report an error, so the
// pattern is banned outright. Render through webrender.RenderHTML instead,
// which buffers first and leaves the response untouched on failure.
func TestNoDirectTemplateExecuteIntoResponse(t *testing.T) {
	// Matches tmpl.Execute(c.Response()…) and tmpl.ExecuteTemplate(c.Response()…),
	// with or without the .Writer suffix — .Writer additionally bypasses Echo's
	// response Size counter, so the access log reports body.size:0.
	banned := regexp.MustCompile(`Execute(Template)?\(\s*c\.Response\(\)`)

	root := filepath.Join("..", "..")
	skip := map[string]bool{".git": true, "node_modules": true, ".local": true}

	var violations []string
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
		// This package is the rule's home, and render_test.go deliberately
		// executes a template straight into a response to pin the stdlib
		// truncation behaviour the rule exists to prevent. RenderHTML itself
		// writes via buf.WriteTo, so it never matches regardless.
		if strings.Contains(filepath.ToSlash(path), "internal/webrender/") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			// A comment naming the pattern is not a violation — this file's own
			// doc comment quotes it, and so may any future explanation of it.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if banned.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, filepath.ToSlash(rel)+":"+itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	for _, v := range violations {
		t.Errorf(
			"%s\n"+
				"  Executing a template straight into c.Response() commits a 200 with a\n"+
				"  truncated body when the render fails partway — silently.\n"+
				"  Use webrender.RenderHTML(c, tmpl, name, data) instead; it buffers first\n"+
				"  and leaves the response untouched on error.",
			v,
		)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
