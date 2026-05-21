package orb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Override records a single locally-overridden field on a config item.
// intentValue is what orbital published; localValue is what the operator set.
// On import, overrides.json is cleared — the new import is the new source of truth.
type Override struct {
	ResourceType  string    `json:"resourceType"`
	ResourceOrbID string    `json:"resourceOrbId"`
	ResourceID    string    `json:"resourceId"` // DGraph ID
	Field         string    `json:"field"`
	IntentValue   string    `json:"intentValue"`
	LocalValue    string    `json:"localValue"`
	OverriddenBy  string    `json:"overriddenBy"`
	OverriddenAt  time.Time `json:"overriddenAt"`
}

// LoadOverrides reads the current overrides.json from dataDir.
// Returns an empty slice (not an error) if the file does not exist.
func LoadOverrides(dataDir string) ([]Override, error) {
	path := filepath.Join(dataDir, overridesFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []Override{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read overrides: %w", err)
	}
	var overrides []Override
	if err := json.Unmarshal(data, &overrides); err != nil {
		return nil, fmt.Errorf("parse overrides: %w", err)
	}
	return overrides, nil
}

// SaveOverride upserts a single override into overrides.json.
// If an override for the same (resourceOrbId, field) already exists, its
// intentValue is preserved (the original intent must not be overwritten).
func SaveOverride(dataDir string, o Override) error {
	existing, err := LoadOverrides(dataDir)
	if err != nil {
		return err
	}

	replaced := false
	for i, e := range existing {
		if e.ResourceOrbID == o.ResourceOrbID && e.Field == o.Field {
			o.IntentValue = e.IntentValue // preserve original intent
			existing[i] = o
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, o)
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal overrides: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir dataDir: %w", err)
	}
	return os.WriteFile(filepath.Join(dataDir, overridesFile), data, 0o644)
}
