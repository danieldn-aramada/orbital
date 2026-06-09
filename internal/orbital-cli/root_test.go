package orbitalcli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/version"
)

func TestRootCommandUsesSharedVersion(t *testing.T) {
	if rootCmd.Version != version.Version {
		t.Errorf("rootCmd.Version = %q, want %q (from internal/version)", rootCmd.Version, version.Version)
	}
}

func TestVersionFlagPrintsInjectedValue(t *testing.T) {
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
	})

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"--version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !strings.Contains(buf.String(), version.Version) {
		t.Errorf("--version output missing %q, got: %q", version.Version, buf.String())
	}
}
