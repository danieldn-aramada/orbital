//go:build darwin

package orbauth

import (
	"encoding/json"
	"fmt"
)

const (
	keychainService = "orbital"
	keychainAccount = "credentials"
)

// KeychainStore persists credentials in the macOS Keychain using
// kSecAttrAccessibleWhenUnlockedThisDeviceOnly — credentials are locked when
// the device is locked, never synced to iCloud, and cannot be migrated to
// another device.
//
// If Fallback is set and the keychain is unavailable (e.g. headless CI),
// operations fall back to the FileStore.
type KeychainStore struct {
	Fallback *FileStore
}

// Save stores the refresh token and identity fields in the keychain.
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

	// Delete any existing item first — SecItemAdd cannot update an existing entry.
	deleteKeychainItem(keychainService, keychainAccount) //nolint:errcheck — not found is fine

	status := addKeychainItem(keychainService, keychainAccount, "Orbital CLI credentials", data)
	if status != 0 {
		if s.Fallback != nil {
			return s.Fallback.Save(creds)
		}
		return fmt.Errorf("keychain save: OSStatus %d", int(status))
	}
	return nil
}

// Load retrieves stored credentials from the keychain.
// Only RefreshToken, Name, and Email are populated — AccessToken will be empty.
func (s *KeychainStore) Load() (*Credentials, error) {
	raw, status := loadKeychainItem(keychainService, keychainAccount)
	if osStatusIsNotFound(status) {
		if s.Fallback != nil {
			return s.Fallback.Load()
		}
		return nil, nil
	}
	if status != 0 {
		if s.Fallback != nil {
			return s.Fallback.Load()
		}
		return nil, fmt.Errorf("keychain load: OSStatus %d", int(status))
	}

	var kd keychainData
	if err := json.Unmarshal(raw, &kd); err != nil {
		return nil, fmt.Errorf("decode keychain credentials: %w", err)
	}
	return &Credentials{
		RefreshToken: kd.RefreshToken,
		Name:         kd.Name,
		Email:        kd.Email,
	}, nil
}

// NewKeychainStore returns a KeychainStore with the given fallback.
func NewKeychainStore(fallback *FileStore) Store {
	return &KeychainStore{Fallback: fallback}
}
