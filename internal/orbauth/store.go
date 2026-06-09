package orbauth

import (
	"encoding/json"
	"fmt"
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

// GetToken returns a valid access token from ~/.orbital/credentials.json.
// Returns an error with instructions to run "orbital login" if no valid token exists.
func GetToken() (string, error) {
	store, err := OrbitalFileStore()
	if err != nil {
		return "", err
	}
	creds, _ := LoadValid(store)
	if creds == nil {
		return "", fmt.Errorf("no valid session — run 'orbital login' to sign in")
	}
	return creds.AccessToken, nil
}
