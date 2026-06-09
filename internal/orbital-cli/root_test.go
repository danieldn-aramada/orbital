package orbitalcli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/version"
)

func TestVersionCommandPrintsInjectedValue(t *testing.T) {
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
	})

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	versionCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !strings.Contains(buf.String(), version.Version) {
		t.Errorf("version output missing %q, got: %q", version.Version, buf.String())
	}
}
