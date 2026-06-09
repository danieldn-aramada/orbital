package orbauth

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
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

// ── getCredentialsFromStore — silent refresh paths ────────────────────────────
//
// Tests substitute refreshFunc with a fake to avoid hitting real Azure AD.
// The package-level var pattern is documented in store.go.

func withFakeRefresh(t *testing.T, fake func(refreshToken, name, email string) (*Credentials, error)) {
	t.Helper()
	orig := refreshFunc
	refreshFunc = fake
	t.Cleanup(func() { refreshFunc = orig })
}

func TestGetCredentialsFromStore_ValidTokenReturnsImmediately(t *testing.T) {
	store := &FileStore{Path: filepath.Join(t.TempDir(), "creds.json")}
	want := &Credentials{
		AccessToken:  "still-good",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
		Email:        "alice@armada.ai",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	called := false
	withFakeRefresh(t, func(_, _, _ string) (*Credentials, error) {
		called = true
		return nil, errors.New("refresh should not be called when token is valid")
	})

	got, err := getCredentialsFromStore(store)
	if err != nil {
		t.Fatalf("getCredentialsFromStore: %v", err)
	}
	if got.AccessToken != "still-good" {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, "still-good")
	}
	if called {
		t.Error("refreshFunc was called for a valid token — fast path is broken")
	}
}

func TestGetCredentialsFromStore_ExpiredWithoutRefreshTokenErrors(t *testing.T) {
	store := &FileStore{Path: filepath.Join(t.TempDir(), "creds.json")}
	if err := store.Save(&Credentials{
		AccessToken:  "expired",
		RefreshToken: "", // no refresh token — refresh path is blocked
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		Email:        "alice@armada.ai",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := getCredentialsFromStore(store)
	if err == nil {
		t.Fatal("expected error when no refresh token available, got nil")
	}
	if !strings.Contains(err.Error(), "run 'orbital login'") {
		t.Errorf("error should instruct user to run 'orbital login', got: %v", err)
	}
}

func TestGetCredentialsFromStore_ExpiredRefreshesAndSaves(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "creds.json")
	store := &FileStore{Path: storePath}
	if err := store.Save(&Credentials{
		AccessToken:  "old-access",
		RefreshToken: "good-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		Name:         "Alice Smith",
		Email:        "alice@armada.ai",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	refreshCalls := 0
	withFakeRefresh(t, func(refreshToken, name, email string) (*Credentials, error) {
		refreshCalls++
		if refreshToken != "good-refresh" {
			t.Errorf("refreshFunc called with refreshToken=%q, want %q", refreshToken, "good-refresh")
		}
		if email != "alice@armada.ai" {
			t.Errorf("refreshFunc called with email=%q, want carry-over", email)
		}
		return &Credentials{
			AccessToken:  "new-access",
			RefreshToken: "rotated-refresh",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
			Name:         name,
			Email:        email,
		}, nil
	})

	got, err := getCredentialsFromStore(store)
	if err != nil {
		t.Fatalf("getCredentialsFromStore: %v", err)
	}
	if got.AccessToken != "new-access" {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, "new-access")
	}
	if refreshCalls != 1 {
		t.Errorf("refreshFunc calls: got %d, want 1", refreshCalls)
	}

	// New credentials must have been persisted, including the rotated refresh token.
	persisted, err := store.Load()
	if err != nil {
		t.Fatalf("Load after refresh: %v", err)
	}
	if persisted.AccessToken != "new-access" {
		t.Errorf("persisted AccessToken: got %q, want %q", persisted.AccessToken, "new-access")
	}
	if persisted.RefreshToken != "rotated-refresh" {
		t.Errorf("persisted RefreshToken: got %q, want rotated value", persisted.RefreshToken)
	}
}

func TestGetCredentialsFromStore_RefreshFailureSurfacesError(t *testing.T) {
	store := &FileStore{Path: filepath.Join(t.TempDir(), "creds.json")}
	if err := store.Save(&Credentials{
		AccessToken:  "old-access",
		RefreshToken: "stale-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		Email:        "alice@armada.ai",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	withFakeRefresh(t, func(_, _, _ string) (*Credentials, error) {
		return nil, fmt.Errorf("invalid_grant: refresh token expired")
	})

	_, err := getCredentialsFromStore(store)
	if err == nil {
		t.Fatal("expected error when refresh fails, got nil")
	}
	if !strings.Contains(err.Error(), "run 'orbital login'") {
		t.Errorf("error should instruct user to run 'orbital login', got: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error should wrap the AAD failure, got: %v", err)
	}
}

func TestGetCredentialsFromStore_MissingFileReportsNoSession(t *testing.T) {
	store := &FileStore{Path: filepath.Join(t.TempDir(), "does-not-exist.json")}

	_, err := getCredentialsFromStore(store)
	if err == nil {
		t.Fatal("expected error for missing credentials file, got nil")
	}
	if !strings.Contains(err.Error(), "no session") {
		t.Errorf("error should say 'no session', got: %v", err)
	}
}
