package orbauth

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

// TestFileStore_RoundTripPreservesRefreshToken guards against regressions
// after the keychain removal — refresh tokens now live in the same file as
// the access token, and must survive Save → Load unchanged.
func TestFileStore_RoundTripPreservesRefreshToken(t *testing.T) {
	store := &FileStore{Path: filepath.Join(t.TempDir(), ".orbital", "credentials.json")}

	want := &Credentials{
		AccessToken:  "access-token-string",
		RefreshToken: "refresh-token-string",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Truncate(time.Second).UTC(),
		Name:         "Alice Example",
		Email:        "alice@armada.ai",
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.AccessToken != want.AccessToken {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, want.AccessToken)
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("RefreshToken: got %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt: got %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	if got.Name != want.Name {
		t.Errorf("Name: got %q, want %q", got.Name, want.Name)
	}
	if got.Email != want.Email {
		t.Errorf("Email: got %q, want %q", got.Email, want.Email)
	}
}

func TestFileStore_DeleteRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	store := &FileStore{Path: path}

	if err := store.Save(&Credentials{AccessToken: "x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := store.Load(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after Delete, Load should return ErrNotExist, got: %v", err)
	}
}

func TestFileStore_DeleteIsIdempotent(t *testing.T) {
	store := &FileStore{Path: filepath.Join(t.TempDir(), "never-existed.json")}

	if err := store.Delete(); err != nil {
		t.Errorf("Delete on missing file should be nil, got: %v", err)
	}
}
