package blobstore

import (
	"context"
	"testing"
)

// Managed identity must select the Azure backend and record the mode — the
// backend is chosen by sniffing the endpoint, so a regression here would
// silently hand an azureStore a blank account key instead.
func TestNewSelectsAzureManagedIdentity(t *testing.T) {
	s, err := New(context.Background(), Config{
		Endpoint:   "https://storbitaldevccwus01.blob.core.windows.net",
		Bucket:     "armada-orbital",
		AccessKey:  "storbitaldevccwus01",
		UseAzureMI: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	az, ok := s.(*azureStore)
	if !ok {
		t.Fatalf("got %T, want *azureStore", s)
	}
	if !az.useAzureMI {
		t.Fatal("useAzureMI not propagated — PresignURL would try to sign with an empty account key")
	}
	if az.accountKey != "" {
		t.Fatalf("accountKey should be empty under managed identity, got %q", az.accountKey)
	}
}

// The shared-key path must keep working: it is the only option for local dev
// and for air-gapped sites that cannot reach Entra ID.
func TestNewKeepsSharedKeyPath(t *testing.T) {
	s, err := New(context.Background(), Config{
		Endpoint:  "https://example.blob.core.windows.net",
		Bucket:    "orbital",
		AccessKey: "example",
		SecretKey: "dGVzdGtleQ==",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	az, ok := s.(*azureStore)
	if !ok {
		t.Fatalf("got %T, want *azureStore", s)
	}
	if az.useAzureMI {
		t.Fatal("useAzureMI should be false when not requested")
	}
}
