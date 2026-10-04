package orbserver

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A UNIT test must never be configured against a live dev DGraph.
//
// `make test-unit` promises "no external services required", and on 2026-10-02
// it was quietly breaking that promise in the worst available way: testCfg
// pointed at orb's real graph on :8082, and TestImportArtifact_ValidZipReturns202
// accepts a bundle and returns 202 — an import being `drop_all` + reload,
// running ASYNCHRONOUSLY after the handler returns. The developer's orb graph
// went empty, three e2e specs started failing, and nothing connected the two.
//
// It was also timing-dependent: run that test alone and the binary exits before
// the goroutine reaches drop_all. It only fires when other tests keep the
// process alive — so it looks harmless under exactly the command someone would
// reach for to investigate it.
//
// Integration tests are exempt: they run under a build tag against the test
// cluster, and testutil.refuseDevGraph already stops them touching a dev one.
func TestUnitTestConfigsNeverTargetDevDGraph(t *testing.T) {
	// Blue, scratch and orb — the graphs a developer is actually using.
	devPorts := regexp.MustCompile(`(localhost|127\.0\.0\.1):(8080|8081|8082|9080|9081|9082)`)

	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.HasPrefix(string(src), "//go:build integration") {
			continue
		}
		for i, line := range strings.Split(string(src), "\n") {
			// Skip comment LINES — this file's own explanation names :8082.
			//
			// Deliberately not `strings.Cut(line, "//")` to strip trailing
			// comments: that splits on the "//" inside "http://" and blanks
			// exactly the lines being looked for, which made an earlier version
			// of this guard pass on a reintroduced :8082. Verified by putting
			// one back and watching it fail.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if m := devPorts.FindString(line); m != "" {
				t.Errorf("%s:%d targets a DEV DGraph (%s) from a unit test — "+
					"use an unroutable address like 127.0.0.1:1, or give the test the "+
					"integration build tag. An async import from here drop_all's "+
					"someone's working graph.\n\t%s", f, i+1, m, strings.TrimSpace(line))
			}
		}
	}
}
