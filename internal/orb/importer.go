package orb

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/armada/orbital/internal/orbconfig"
)

const (
	importHistoryFile = "import-history.json"
	overridesFile     = "overrides.json"
	historyMaxRecords = 25
	scratchFile       = "data.json.gz"
	SchemaFile        = "schema.graphql"
)

// ImportMeta carries metadata from OCI manifest annotations for a pulled artifact.
type ImportMeta struct {
	Tag         string
	Digest      string
	DCOrbID     string
	ExportJobID string
	CreatedAt   time.Time
}

// ImportRecord is one entry in the import history log.
type ImportRecord struct {
	Tag         string    `json:"tag"`
	Digest      string    `json:"digest"`
	DCOrbID     string    `json:"dcOrbId"`
	ExportJobID string    `json:"exportJobId"`
	ImportedAt  time.Time `json:"importedAt"`
	Status      string    `json:"status"` // "done" | "failed"
	Error       string    `json:"error,omitempty"`
}

// Importer executes the full import pipeline: pull → verify → drop_all → schema → dgraph live.
type Importer struct {
	cfg    orbconfig.Config
	logger *slog.Logger
}

// NewImporter creates an Importer with the given config and logger.
func NewImporter(cfg orbconfig.Config, logger *slog.Logger) *Importer {
	return &Importer{cfg: cfg, logger: logger}
}

// Import executes the full import sequence for a pulled artifact:
//  1. drop_all on local DGraph Alpha
//  2. Apply schema.gz to DGraph admin
//  3. Write data.json.gz to scratch volume
//  4. Exec: dgraph live -f /tmp/orb-import/data.json.gz -a localhost:9080 inside dgraph-orb-alpha
//  5. Clear overrides.json (new import resets all local overrides)
//  6. Record import in history file
func (i *Importer) Import(ctx context.Context, dataGZ, schemaGZ []byte, meta ImportMeta) error {
	shortDigest := meta.Digest
	if len(shortDigest) > 12 {
		shortDigest = shortDigest[:12]
	}
	i.logger.Info("import starting", "tag", meta.Tag, "digest", shortDigest)

	if err := i.dropAll(ctx); err != nil {
		return fmt.Errorf("drop_all: %w", err)
	}

	if err := i.applySchema(ctx, schemaGZ); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	scratchPath := filepath.Join(i.cfg.DataDir, scratchFile)
	if err := os.MkdirAll(i.cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.WriteFile(scratchPath, dataGZ, 0o644); err != nil {
		return fmt.Errorf("write scratch file: %w", err)
	}

	if err := i.dgraphLive(ctx); err != nil {
		return fmt.Errorf("dgraph live: %w", err)
	}

	// New import resets all local overrides — the imported intent is now authoritative.
	overridesPath := filepath.Join(i.cfg.DataDir, overridesFile)
	if _, err := os.Stat(overridesPath); err == nil {
		i.logger.Warn("clearing overrides.json — new import resets all local overrides")
		if err := os.Remove(overridesPath); err != nil {
			i.logger.Warn("failed to clear overrides.json", "err", err)
		}
	}

	if err := i.recordHistory(meta, "done", ""); err != nil {
		i.logger.Warn("failed to record import history", "err", err)
	}

	i.logger.Info("import complete", "tag", meta.Tag)
	return nil
}

// dropAll sends a DGraph drop_all operation to reset the local graph.
func (i *Importer) dropAll(ctx context.Context) error {
	i.logger.Info("drop_all on local DGraph")
	body := []byte(`{"drop_all": true}`)
	alterURL := strings.TrimSuffix(i.cfg.DGraphAdminURL, "/admin") + "/alter"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, alterURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("drop_all returned %d: %s", resp.StatusCode, b)
	}
	return nil
}

// applySchema decompresses schemaGZ, posts it to DGraph's admin schema endpoint,
// and saves the decompressed SDL to {DataDir}/schema.graphql for the schema page.
func (i *Importer) applySchema(ctx context.Context, schemaGZ []byte) error {
	i.logger.Info("applying schema to local DGraph")
	gr, err := gzip.NewReader(bytes.NewReader(schemaGZ))
	if err != nil {
		return fmt.Errorf("decompress schema: %w", err)
	}
	schema, err := io.ReadAll(gr)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	gr.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.cfg.DGraphAdminURL+"/schema", bytes.NewReader(schema))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/graphql")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("schema apply returned %d: %s", resp.StatusCode, b)
	}

	schemaPath := filepath.Join(i.cfg.DataDir, SchemaFile)
	if err := os.WriteFile(schemaPath, schema, 0o644); err != nil {
		i.logger.Warn("failed to save schema to disk", "err", err)
	}

	return nil
}

// dgraphLive copies the scratch file into the container then execs `dgraph live`.
// Using docker cp rather than a shared volume so orb works correctly when running
// outside Docker (local dev via make run-orb) as well as inside Docker.
func (i *Importer) dgraphLive(ctx context.Context) error {
	scratchPath := filepath.Join(i.cfg.DataDir, scratchFile)
	i.logger.Info("copying scratch file into container", "container", i.cfg.DGraphContainerName)
	if err := dockerCopy(ctx, i.cfg.DGraphContainerName, scratchPath, "/tmp/orb-import/"); err != nil {
		return fmt.Errorf("docker cp: %w", err)
	}

	i.logger.Info("running dgraph live inside container", "container", i.cfg.DGraphContainerName)
	cmd := []string{
		"dgraph", "live",
		"-f", "/tmp/orb-import/" + scratchFile,
		"-a", "localhost:9080",
		"-z", "localhost:5080",
	}
	out, err := dockerExec(ctx, i.cfg.DGraphContainerName, cmd)
	if err != nil {
		i.logger.Error("dgraph live failed", "output", out, "err", err)
		return err
	}
	i.logger.Info("dgraph live completed", "output_len", len(out))
	return nil
}

// recordHistory appends an ImportRecord to the rolling history file.
func (i *Importer) recordHistory(meta ImportMeta, status, errMsg string) error {
	path := filepath.Join(i.cfg.DataDir, importHistoryFile)

	var records []ImportRecord
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &records) //nolint:errcheck
	}

	records = append(records, ImportRecord{
		Tag:         meta.Tag,
		Digest:      meta.Digest,
		DCOrbID:     meta.DCOrbID,
		ExportJobID: meta.ExportJobID,
		ImportedAt:  time.Now().UTC(),
		Status:      status,
		Error:       errMsg,
	})

	// Rolling window — keep newest historyMaxRecords entries.
	if len(records) > historyMaxRecords {
		records = records[len(records)-historyMaxRecords:]
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadHistory reads the import history from disk. Returns empty slice if none exists.
func LoadHistory(dataDir string) ([]ImportRecord, error) {
	path := filepath.Join(dataDir, importHistoryFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []ImportRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	return records, nil
}

