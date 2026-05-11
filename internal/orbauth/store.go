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
// This stores only the access token and expiry — the refresh token lives in the keychain.
func OrbitalFileStore() (*FileStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &FileStore{Path: filepath.Join(home, ".orbital", "credentials.json")}, nil
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
