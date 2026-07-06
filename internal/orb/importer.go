package orb

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/armada/orbital/internal/ocitype"
	"github.com/armada/orbital/internal/orb/store"
	"github.com/armada/orbital/internal/orb/store/importrecord"
	"github.com/armada/orbital/internal/orbconfig"
)

// ociDigest returns the OCI-formatted sha256 digest of b ("sha256:<64hex>").
// OCI layer descriptor digest = sha256 of the layer payload, so computing here
// at import time produces the same value the puller captured from the manifest.
func ociDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

const (
	scratchFile = "data.json.gz"

	// Verification states for ImportRecord.Verification.
	VerificationVerified      = "verified"       // cosign-verified via OCI pull
	VerificationUnverified    = "unverified"     // OCI pull but verification failed
	VerificationNotApplicable = "not-applicable" // courier/API upload — no signature available

	// Layer roles for LayerRecord.Role.
	LayerRoleGraph      = "graph"      // consumed by DGraph (always)
	LayerRoleDispatched = "dispatched" // dispatched to a registered consumer
	LayerRoleUnknown    = "unknown"    // no registered consumer — silently skipped

	// graph layer media types — used when recording history entries.
	layerMediaTypeData   = "application/vnd.orbital.subgraph.data.v1+gzip"
	layerMediaTypeSchema = "application/vnd.orbital.subgraph.schema.v1+gzip"
)

// ImportMeta carries metadata for a pulled or uploaded artifact.
type ImportMeta struct {
	Tag          string
	Digest       string
	DCOrbID      string
	ExportJobID  string
	CreatedAt    time.Time
	Verification string // one of the Verification* constants
}

// LayerRecord describes one layer in an imported artifact.
type LayerRecord struct {
	MediaType string          `json:"mediaType"`
	Role      string          `json:"role"`               // LayerRole* constant
	Producer  string          `json:"producer,omitempty"` // from OCI annotation com.armada.orbital.producer (empty for legacy)
	SizeBytes int64           `json:"sizeBytes,omitempty"`
	Digest    string          `json:"digest,omitempty"`
	Dispatch  *DispatchResult `json:"dispatch,omitempty"` // set when Role == LayerRoleDispatched
}

// SizeDisplay returns a human-readable size for the layer (e.g. "1.7 KB").
// Templates call this directly: {{.SizeDisplay}}.
func (r LayerRecord) SizeDisplay() string {
	n := r.SizeBytes
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	case n > 0:
		return fmt.Sprintf("%d B", n)
	default:
		return "—"
	}
}

// ImportRecord is one entry in the import history log.
type ImportRecord struct {
	Tag          string        `json:"tag"`
	Digest       string        `json:"digest"`
	DCOrbID      string        `json:"dcOrbId"`
	ExportJobID  string        `json:"exportJobId"`
	ImportedAt   time.Time     `json:"importedAt"`
	Status       string        `json:"status"`       // "done" | "partial" | "failed". "partial" means the graph import succeeded but at least one extra-layer dispatch failed.
	Verification string        `json:"verification"` // Verification* constant
	Error        string        `json:"error,omitempty"`
	Layers       []LayerRecord `json:"layers,omitempty"`
}

// DispatchErrors returns all dispatch error strings across layers, for use in templates.
func (r ImportRecord) DispatchErrors() []string {
	var errs []string
	for _, l := range r.Layers {
		if l.Role == LayerRoleDispatched && l.Dispatch != nil && l.Dispatch.Error != "" {
			errs = append(errs, l.Dispatch.Error)
		}
	}
	return errs
}

// Importer executes the full import pipeline: pull → verify → drop_all → schema → dgraph live.
type Importer struct {
	cfg     orbconfig.Config
	logger  *slog.Logger
	backend DGraphBackend
	db      *store.Client
}

// NewImporter creates an Importer with the given config, logger, DGraph
// backend, and orb store client. `db` is required — import history is
// persisted through it; nil is a programming error.
func NewImporter(cfg orbconfig.Config, logger *slog.Logger, backend DGraphBackend, db *store.Client) *Importer {
	return &Importer{cfg: cfg, logger: logger, backend: backend, db: db}
}

// Import executes the full import sequence for a pulled artifact:
//  1. drop_all on local DGraph Alpha
//  2. Apply schema.gz to DGraph admin
//  3. Write data.json.gz to scratch volume
//  4. Exec: dgraph live -f /tmp/orb-import/data.json.gz -a localhost:9080 inside dgraph-orb-alpha
//  5. Record import in history (SQLite)
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

	i.logger.Info("running dgraph live", "path", scratchPath)
	out, err := i.backend.RunLive(ctx, scratchPath)
	if err != nil {
		i.logger.Error("dgraph live failed", "output", out, "err", err)
		return fmt.Errorf("dgraph live: %w", err)
	}
	i.logger.Info("dgraph live completed", "output_len", len(out))

	if err := i.recordHistory(ctx, meta, dataGZ, schemaGZ, "done", ""); err != nil {
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

// applySchema decompresses schemaGZ and posts it to DGraph's admin schema
// endpoint. The schema becomes the single source of truth — orb's schema page
// queries `getGQLSchema` from DGraph at render time rather than reading a
// sidecar file. Removed the previous {DataDir}/schema.graphql write 2026-06-20
// because the sidecar file silently drifted from DGraph state when DGraph was
// wiped out-of-band (e.g. `make down` + `make up`), leaving the file claiming
// a schema was loaded while DGraph itself was empty.
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

	return nil
}

// recordHistory inserts one ImportRecord row into orb's SQLite store.
// dataGZ + schemaGZ are passed in so Size + Digest can be populated on the
// graph layer records — these are the same bytes Import() applied to DGraph,
// so their sha256 equals the OCI layer descriptor digest captured at pull time.
func (i *Importer) recordHistory(ctx context.Context, meta ImportMeta, dataGZ, schemaGZ []byte, status, errMsg string) error {
	layers := []LayerRecord{
		// Graph layers are by definition produced by orbital. Using the canonical
		// "orbital" producer string keeps orb's UI consistent with the annotation
		// value that the publisher writes on the same layers.
		{MediaType: layerMediaTypeData, Role: LayerRoleGraph, Producer: ocitype.ProducerOrbital, SizeBytes: int64(len(dataGZ)), Digest: ociDigest(dataGZ)},
		{MediaType: layerMediaTypeSchema, Role: LayerRoleGraph, Producer: ocitype.ProducerOrbital, SizeBytes: int64(len(schemaGZ)), Digest: ociDigest(schemaGZ)},
	}
	layersJSON, err := json.Marshal(layers)
	if err != nil {
		return fmt.Errorf("marshal layers: %w", err)
	}

	create := i.db.ImportRecord.Create().
		SetID(uuid.New()).
		SetTag(meta.Tag).
		SetDigest(meta.Digest).
		SetDcOrbID(meta.DCOrbID).
		SetExportJobID(meta.ExportJobID).
		SetImportedAt(time.Now().UTC()).
		SetStatus(importRecordStatus(status)).
		SetLayersJSON(string(layersJSON))
	if v := importRecordVerification(meta.Verification); v != "" {
		create = create.SetVerification(v)
	}
	if errMsg != "" {
		create = create.SetError(errMsg)
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("insert import record: %w", err)
	}
	return nil
}

// FinalizeLastHistory appends dispatch layer records and optionally overrides
// the status on the most recent ImportRecord row. Called by the importArtifact
// handler after Import() writes the base record and Dispatcher.Dispatch()
// completes. Pass status="" to leave the existing status untouched.
// Safe to call because the import state machine prevents concurrent imports.
func FinalizeLastHistory(ctx context.Context, db *store.Client, layers []LayerRecord, status string) error {
	if len(layers) == 0 && status == "" {
		return nil
	}
	row, err := db.ImportRecord.Query().
		Order(store.Desc(importrecord.FieldImportedAt)).
		First(ctx)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load last history: %w", err)
	}

	var existing []LayerRecord
	if row.LayersJSON != "" {
		if err := json.Unmarshal([]byte(row.LayersJSON), &existing); err != nil {
			return fmt.Errorf("unmarshal layers_json: %w", err)
		}
	}
	if len(layers) > 0 {
		existing = append(existing, layers...)
	}
	newLayers, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal layers: %w", err)
	}
	upd := db.ImportRecord.UpdateOneID(row.ID).SetLayersJSON(string(newLayers))
	if status != "" {
		upd = upd.SetStatus(importRecordStatus(status))
	}
	if _, err := upd.Save(ctx); err != nil {
		return fmt.Errorf("update history row: %w", err)
	}
	return nil
}

// LoadHistory returns import history rows in ascending imported_at order
// (oldest first, matching the pre-migration file-append contract). Callers
// that want newest-first reverse the slice at their layer.
func LoadHistory(ctx context.Context, db *store.Client) ([]ImportRecord, error) {
	rows, err := db.ImportRecord.Query().
		Order(store.Asc(importrecord.FieldImportedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load history: %w", err)
	}
	out := make([]ImportRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, importRecordFromRow(r))
	}
	return out, nil
}

func importRecordFromRow(r *store.ImportRecord) ImportRecord {
	rec := ImportRecord{
		Tag:          r.Tag,
		Digest:       r.Digest,
		DCOrbID:      r.DcOrbID,
		ExportJobID:  r.ExportJobID,
		ImportedAt:   r.ImportedAt,
		Status:       string(r.Status),
		Verification: string(r.Verification),
		Error:        r.Error,
	}
	if r.LayersJSON != "" {
		_ = json.Unmarshal([]byte(r.LayersJSON), &rec.Layers)
	}
	return rec
}

func importRecordStatus(s string) importrecord.Status {
	switch s {
	case "done":
		return importrecord.StatusDone
	case "partial":
		return importrecord.StatusPartial
	case "failed":
		return importrecord.StatusFailed
	default:
		return importrecord.StatusDone
	}
}

func importRecordVerification(v string) importrecord.Verification {
	switch v {
	case VerificationVerified:
		return importrecord.VerificationVerified
	case VerificationUnverified:
		return importrecord.VerificationUnverified
	case VerificationNotApplicable:
		return importrecord.VerificationNotApplicable
	default:
		return ""
	}
}
