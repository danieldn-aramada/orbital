//go:build !darwin

package orbauth

import (
	"encoding/json"
	"fmt"

	"github.com/zalando/go-keyring"
)

const (
	keychainService = "orbital"
	keychainAccount = "credentials"
)

// KeychainStore persists credentials in the OS keychain.
// On Linux it uses libsecret (Secret Service); on Windows it uses the
// Credential Manager.
//
// If Fallback is set and the keychain is unavailable, operations fall back to
// the FileStore. This keeps the CLI functional in headless / CI environments.
type KeychainStore struct {
	Fallback *FileStore
}

// Load retrieves stored credentials from the keychain. Only RefreshToken,
// Name, and Email are populated — AccessToken will be empty and must be
// obtained via RefreshToken before use.
func (s *KeychainStore) Load() (*Credentials, error) {
	secret, err := keyring.Get(keychainService, keychainAccount)
	if err != nil {
		if s.Fallback != nil {
			return s.Fallback.Load()
		}
		return nil, fmt.Errorf("keychain get: %w", err)
	}
	var kd keychainData
	if err := json.Unmarshal([]byte(secret), &kd); err != nil {
		return nil, fmt.Errorf("decode keychain credentials: %w", err)
	}
	return &Credentials{
		RefreshToken: kd.RefreshToken,
		Name:         kd.Name,
		Email:        kd.Email,
	}, nil
}

// Save stores only the refresh token and identity fields in the keychain.
func (s *KeychainStore) Save(creds *Credentials) error {
	kd := keychainData{
		RefreshToken: creds.RefreshToken,
		Name:         creds.Name,
		Email:        creds.Email,
	}
	data, err := json.Marshal(kd)
	if err != nil {
		return err
	}
	if err := keyring.Set(keychainService, keychainAccount, string(data)); err != nil {
		if s.Fallback != nil {
			return s.Fallback.Save(creds)
		}
		return fmt.Errorf("keychain set: %w", err)
	}
	return nil
}

// Delete removes the keychain entry. Safe to call when not logged in.
func (s *KeychainStore) Delete() error {
	err := keyring.Delete(keychainService, keychainAccount)
	if err != nil && err != keyring.ErrNotFound {
		if s.Fallback != nil {
			return s.Fallback.Delete()
		}
		return fmt.Errorf("keychain delete: %w", err)
	}
	if s.Fallback != nil {
		_ = s.Fallback.Delete()
	}
	return nil
}

// NewKeychainStore returns a KeychainStore with the given fallback.
func NewKeychainStore(fallback *FileStore) Store {
	return &KeychainStore{Fallback: fallback}
}
