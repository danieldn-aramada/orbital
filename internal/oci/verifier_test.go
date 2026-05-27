package oci

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestVerify_EmptyPublicKeyPath(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	_, err := Verify(context.Background(), VerifyConfig{PublicKeyPath: ""}, "registry/repo", "sha256:abc", logger)
	if err == nil {
		t.Fatal("expected error for empty PublicKeyPath, got nil")
	}
	if !strings.Contains(err.Error(), "ORB_OCI_PUBLIC_KEY_PATH") {
		t.Errorf("expected error to mention ORB_OCI_PUBLIC_KEY_PATH, got: %v", err)
	}
}
