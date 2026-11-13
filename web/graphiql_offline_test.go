package web

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var (
	htmlComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlExternal = regexp.MustCompile(`https?://|(?:src|href)\s*=\s*["']?//`)
	jsExternal   = regexp.MustCompile("(?:\\bfrom|\\bimport|importScripts|new\\s+(?:Shared)?Worker|new\\s+URL|fetch)\\s*\\(?\\s*[\"'`](?:https?:)?//")
	cssExternal  = regexp.MustCompile(`url\(\s*["']?(?:https?:)?//|@import\s+(?:url\()?\s*["']?(?:https?:)?//`)
)

// TestGraphiQLAssetsReferenceNoExternalHosts fails if the GraphiQL page loads
// anything from another host: that works on a connected laptop and breaks only
// at an air-gapped site.
func TestGraphiQLAssetsReferenceNoExternalHosts(t *testing.T) {
	bundled, err := filepath.Glob(filepath.Join("shared", "static", "graphiql-*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundled) == 0 {
		t.Fatal("no bundled GraphiQL under shared/static/graphiql-*/ — run make vendor-graphiql")
	}

	for _, path := range append([]string{filepath.Join("shared", "static", "graphiql.html")}, bundled...) {
		var pattern *regexp.Regexp
		switch filepath.Ext(path) {
		case ".html":
			pattern = htmlExternal
		case ".js":
			pattern = jsExternal
		case ".css":
			pattern = cssExternal
		default:
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if pattern == htmlExternal {
			src = htmlComment.ReplaceAllString(src, "")
		}
		if loc := pattern.FindStringIndex(src); loc != nil {
			end := min(loc[1]+80, len(src))
			t.Errorf("%s loads from an external host: %q", path, src[loc[0]:end])
		}
	}
}
