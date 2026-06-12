package divergence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MediaTypeMapping is the OCI media type for the K8s-path → orbital-orbId
// mapping layer emitted by cb-bundler. orb stores layers of this type
// under DataDir/mappings/<digest>.json instead of dispatching them.
const MediaTypeMapping = "application/vnd.armada.configbundle.mapping.v1+json"

// MappingItem is one entry in the bundle's mapping layer. It pairs a K8s
// field-path prefix with the orbital orbId for the ConfigItem that owns
// fields under that prefix. The leaf segment of the matched path becomes
// the orbital field name at resolve time. Type carries the orbital GraphQL
// type name so orbital can dispatch an `update{Type}` mutation when an
// admin accepts the override.
type MappingItem struct {
	Path  string `json:"path"`           // e.g. "spec.servers[serviceTag=3RK3V64].idrac"
	OrbID string `json:"orbId"`          // e.g. "colo:srv-001-idrac"
	Type  string `json:"type,omitempty"` // e.g. "IdracSettings" — empty for legacy bundles
}

// Mapping is the deserialized mapping.json layer for a single bundle.
type Mapping struct {
	BundleDigest string        `json:"bundleDigest"`
	Items        []MappingItem `json:"items"`

	// sortedItems is Items ordered by descending path length so prefix
	// matching can short-circuit on the first match. Built once on load.
	sortedItems []MappingItem
}

// Resolve walks the longest-prefix MappingItem that prefixes the given path
// and returns the corresponding orbId and the leaf field name (the path
// remainder after the prefix and the separating dot). Returns an error when
// no prefix matches.
//
// Domain-agnostic: knows nothing about which fields are list-map keys, only
// about string prefixes. cb-bundler and cb-controller agree on the path
// format; orb just matches strings.
func (m *Mapping) Resolve(path string) (orbID, field, typeName string, err error) {
	if m == nil {
		return "", "", "", errors.New("nil mapping")
	}
	for _, item := range m.sortedItems {
		switch {
		case path == item.Path:
			// Exact match — no leaf field, which is invalid for a divergence entry.
			return "", "", "", fmt.Errorf("path %q matches a ConfigItem boundary, not a field", path)
		case strings.HasPrefix(path, item.Path+"."):
			leaf := path[len(item.Path)+1:]
			// A leaf with structure (`.` or `[`) means the matched prefix was
			// too shallow — cb-bundler should have emitted a deeper mapping
			// entry for the intermediate ConfigItem. Refuse rather than emit
			// a malformed field name into the divergence report.
			if strings.ContainsAny(leaf, ".[") {
				return "", "", "", fmt.Errorf("matched prefix %q for path %q is too shallow; leaf %q is not a simple field name (mapping is missing an intermediate entry)", item.Path, path, leaf)
			}
			return item.OrbID, leaf, item.Type, nil
		}
	}
	return "", "", "", fmt.Errorf("no mapping prefix matches path %q", path)
}

// LoadMapping reads a mapping JSON file from disk.
func LoadMapping(path string) (*Mapping, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mapping: %w", err)
	}
	return ParseMapping(b)
}

// ParseMapping deserializes a mapping JSON payload and validates it.
func ParseMapping(b []byte) (*Mapping, error) {
	var m Mapping
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("decode mapping: %w", err)
	}
	if len(m.Items) == 0 {
		return nil, errors.New("mapping has no items")
	}
	// Sort by descending path length so longest-prefix wins on first hit.
	m.sortedItems = make([]MappingItem, len(m.Items))
	copy(m.sortedItems, m.Items)
	sort.SliceStable(m.sortedItems, func(i, j int) bool {
		return len(m.sortedItems[i].Path) > len(m.sortedItems[j].Path)
	})
	return &m, nil
}

// MappingStore manages mapping files on disk under DataDir/mappings/.
type MappingStore struct {
	dir string
}

func NewMappingStore(dataDir string) *MappingStore {
	return &MappingStore{dir: filepath.Join(dataDir, "mappings")}
}

// Save writes the raw mapping JSON payload for the given bundle digest.
// The payload is stored byte-for-byte as received — no re-encoding — so
// future tooling can verify the exact bytes that arrived in the bundle.
func (s *MappingStore) Save(digest string, payload []byte) error {
	if digest == "" {
		return errors.New("empty digest")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("mkdir mappings: %w", err)
	}
	// Validate the payload before writing — refuse to persist garbage.
	if _, err := ParseMapping(payload); err != nil {
		return fmt.Errorf("validate mapping: %w", err)
	}
	return os.WriteFile(s.pathFor(digest), payload, 0o644)
}

// Load returns the parsed mapping for the given bundle digest.
func (s *MappingStore) Load(digest string) (*Mapping, error) {
	if digest == "" {
		return nil, errors.New("empty digest")
	}
	return LoadMapping(s.pathFor(digest))
}

// Has returns true when a mapping file exists for the given digest.
func (s *MappingStore) Has(digest string) bool {
	_, err := os.Stat(s.pathFor(digest))
	return err == nil
}

// Delete removes the mapping file for the given digest. Used when pruning
// old bundle history. Idempotent — missing file is not an error.
func (s *MappingStore) Delete(digest string) error {
	err := os.Remove(s.pathFor(digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// pathFor returns the on-disk path for a given digest. The digest is used
// as-is; orb only ever sees fully-formed sha256:<hex> strings from the OCI
// import flow, and macOS/Linux filesystems handle the colon fine.
func (s *MappingStore) pathFor(digest string) string {
	return filepath.Join(s.dir, digest+".json")
}
