package orbauth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Store persists and loads credentials.
type Store interface {
	Load() (*Credentials, error)
	Save(creds *Credentials) error
	Delete() error
}

// FileStore persists credentials as JSON at a local file path.
type FileStore struct {
	Path string
}

func (s *FileStore) Load() (*Credentials, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

func (s *FileStore) Delete() error {
	err := os.Remove(s.Path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *FileStore) Save(creds *Credentials) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, data, 0600)
}

// OrbFileStore returns a FileStore pointing at ~/.orb/credentials.json.
func OrbFileStore() (*FileStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &FileStore{Path: filepath.Join(home, ".orb", "credentials.json")}, nil
}

// OrbitalFileStore returns a FileStore pointing at ~/.orbital/credentials.json.
// Stores the full credentials blob: access token, refresh token, and identity.
func OrbitalFileStore() (*FileStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &FileStore{Path: filepath.Join(home, ".orbital", "credentials.json")}, nil
}

// ClearOrbitalCredentials removes the credentials file, signing the user out.
// Safe to call when already logged out.
func ClearOrbitalCredentials() error {
	fileStore, err := OrbitalFileStore()
	if err != nil {
		return err
	}
	if err := fileStore.Delete(); err != nil {
		return fmt.Errorf("remove credentials file: %w", err)
	}
	return nil
}

// refreshFunc is the function used to exchange a refresh token for new
// credentials. Package-level for test substitution; production code never
// reassigns it.
var refreshFunc = RefreshToken

// GetToken returns a valid access token, silently refreshing if the cached
// access token is expired and a refresh token is available. Returns an error
// only when no session is available or refresh fails — both cases instruct
// the caller to run 'orbital login'.
func GetToken() (string, error) {
	creds, err := GetCredentials()
	if err != nil {
		return "", err
	}
	return creds.AccessToken, nil
}

// GetCredentials returns the full credentials blob (access token, refresh
// token, identity) for the current session, silently refreshing if needed.
// Callers that need user identity (e.g., recording "updatedBy" in a mutation)
// should use this instead of GetToken.
func GetCredentials() (*Credentials, error) {
	store, err := OrbitalFileStore()
	if err != nil {
		return nil, err
	}
	return getCredentialsFromStore(store)
}

// getCredentialsFromStore is the testable core of GetCredentials. Tests inject
// a store backed by a temp dir and override refreshFunc to avoid real AAD calls.
func getCredentialsFromStore(store Store) (*Credentials, error) {
	creds, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("no session — run 'orbital login' to sign in")
	}

	// Fast path: access token still valid (more than 60s remaining).
	if creds.Valid() {
		slog.Debug("orbauth: cached access token still valid")
		return creds, nil
	}

	// Slow path: access token expired. Need a refresh token to recover.
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("session expired — run 'orbital login' to sign in")
	}

	slog.Debug("orbauth: access token expired, attempting silent refresh")
	newCreds, err := refreshFunc(creds.RefreshToken, creds.Name, creds.Email)
	if err != nil {
		return nil, fmt.Errorf("session expired and refresh failed — run 'orbital login' to sign in: %w", err)
	}

	// Persist new credentials. Save failures are non-fatal — the in-memory
	// token works for the current operation, and the next call will refresh
	// again. Better than failing a working operation over disk trouble.
	if saveErr := store.Save(newCreds); saveErr != nil {
		slog.Warn("orbauth: could not save refreshed credentials", "err", saveErr)
	} else {
		slog.Debug("orbauth: refreshed credentials saved")
	}
	return newCreds, nil
}
