package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/restorejob"
	"github.com/armada/orbital/internal/blobstore"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// RestoreHandler handles async DGraph restore operations.
type RestoreHandler struct {
	db               *ent.Client
	storage          blobstore.Store
	backend          RestoreBackend
	dgraphAlterURL   string // e.g. http://dgraph-blue:8080/alter
	dgraphSchemaURL  string // e.g. http://dgraph-blue:8080/admin/schema
	dgraphGraphQLURL string // e.g. http://dgraph-blue:8080/graphql — used to enumerate affected DCs post-restore
	dgraphAlphaGRPC  string // e.g. dgraph-blue-dgraph-alpha:9080
	dgraphZeroGRPC   string // e.g. dgraph-blue-dgraph-zero:5080
	schemaPath       string // path to the GraphQL SDL schema file
	restoreTimeout   time.Duration
	logger           *slog.Logger
}

// RestoreConfig holds configuration for the restore handler.
type RestoreConfig struct {
	S3Endpoint      string
	S3Region        string
	S3Bucket        string
	S3AccessKey     string
	S3SecretKey     string
	DGraphAdminURL  string // e.g. http://dgraph-blue:8080/admin
	DGraphAlphaGRPC string // gRPC address of DGraph alpha, e.g. dgraph-blue-dgraph-alpha:9080
	DGraphZeroGRPC  string // gRPC address of DGraph zero, e.g. dgraph-blue-dgraph-zero:5080
	SchemaPath      string // path to the GraphQL SDL schema file
	RestoreTimeout  time.Duration
}

func NewRestoreHandler(ctx context.Context, db *ent.Client, cfg RestoreConfig, backend RestoreBackend, logger *slog.Logger) (*RestoreHandler, error) {
	store, err := blobstore.New(ctx, blobstore.Config{
		Endpoint:  cfg.S3Endpoint,
		Region:    cfg.S3Region,
		Bucket:    cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
	})
	if err != nil {
		return nil, err
	}

	base := strings.TrimSuffix(cfg.DGraphAdminURL, "/admin")

	return &RestoreHandler{
		db:               db,
		storage:          store,
		backend:          backend,
		dgraphAlterURL:   base + "/alter",
		dgraphSchemaURL:  base + "/admin/schema",
		dgraphGraphQLURL: base + "/graphql",
		dgraphAlphaGRPC:  cfg.DGraphAlphaGRPC,
		dgraphZeroGRPC:   cfg.DGraphZeroGRPC,
		schemaPath:       cfg.SchemaPath,
		restoreTimeout:   cfg.RestoreTimeout,
		logger:           logger,
	}, nil
}

// fetchAllDCOrbIDs queries the (just-restored) DGraph for every DataCenter
// orbId. Used to attach affected resources to the restoreBackup audit event
// so resource-scoped audit panels (DC, Server) show the restore as context.
// Returns empty on error rather than failing the restore — losing the resource
// attachment is worse UX than failing the operation.
func (h *RestoreHandler) fetchAllDCOrbIDs(ctx context.Context) []string {
	body, err := json.Marshal(map[string]string{
		"query": "{ queryDataCenter { orbId } }",
	})
	if err != nil {
		h.logger.Warn("restore audit: marshal DC query failed", "err", err)
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphGraphQLURL, bytes.NewReader(body))
	if err != nil {
		h.logger.Warn("restore audit: build DC query request failed", "err", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.logger.Warn("restore audit: DC query request failed", "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		h.logger.Warn("restore audit: DC query HTTP error", "status", resp.StatusCode)
		return nil
	}
	var parsed struct {
		Data struct {
			QueryDataCenter []struct {
				OrbID string `json:"orbId"`
			} `json:"queryDataCenter"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		h.logger.Warn("restore audit: decode DC query failed", "err", err)
		return nil
	}
	out := make([]string, 0, len(parsed.Data.QueryDataCenter))
	for _, dc := range parsed.Data.QueryDataCenter {
		if dc.OrbID != "" {
			out = append(out, dc.OrbID)
		}
	}
	return out
}

type restoreJobResponse struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	BackupID    *string `json:"backupId,omitempty"`
	BackupKey   *string `json:"backupKey,omitempty"`
	Log         *string `json:"log,omitempty"`
	Error       *string `json:"error,omitempty"`
	CreatedBy   string  `json:"createdBy"`
	CreatedAt   string  `json:"createdAt"`
	StartedAt   *string `json:"startedAt,omitempty"`
	CompletedAt *string `json:"completedAt,omitempty"`
}

func toRestoreJobResponse(j *ent.RestoreJob) restoreJobResponse {
	r := restoreJobResponse{
		ID:        j.ID.String(),
		Status:    string(j.Status),
		CreatedBy: j.CreatedBy,
		CreatedAt: j.CreatedAt.UTC().Format(time.RFC3339),
	}
	if j.BackupID != nil {
		s := j.BackupID.String()
		r.BackupID = &s
	}
	if j.BackupKey != nil {
		r.BackupKey = j.BackupKey
	}
	if j.Log != nil {
		r.Log = j.Log
	}
	if j.Error != nil {
		r.Error = j.Error
	}
	if j.StartedAt != nil {
		s := j.StartedAt.UTC().Format(time.RFC3339)
		r.StartedAt = &s
	}
	if j.CompletedAt != nil {
		s := j.CompletedAt.UTC().Format(time.RFC3339)
		r.CompletedAt = &s
	}
	return r
}

// Trigger handles POST /api/v1/restore
//
// @Summary     Trigger a DGraph restore
// @Description Restores DGraph blue from a stored backup. Blocked if any backup, export, or restore job is in progress.
// @Tags        backup graph
// @Accept      json
// @Produce     json
// @Param       body body object true "backupId (UUID)"
// @Success     202 {object} triggerResponse
// @Failure     400 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /api/v1/restore [post]
func (h *RestoreHandler) Trigger(c echo.Context) error {
	var req struct {
		BackupID              string `json:"backupId"`
		ConfirmSchemaMismatch bool   `json:"confirmSchemaMismatch"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.ErrBadRequest
	}
	backupUUID, err := uuid.Parse(req.BackupID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid backupId"})
	}

	ctx := c.Request().Context()

	existingBackup, err := h.db.Backup.Query().
		Where(backup.StatusIn(backup.StatusPending, backup.StatusRunning)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("check backup jobs: %w", err)
	}
	if existingBackup != nil {
		return c.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("backup in progress (id: %s)", existingBackup.ID),
		})
	}

	existingExport, err := h.db.ExportJob.Query().
		Where(exportjob.StatusIn(exportjob.StatusPending, exportjob.StatusRunning)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("check export jobs: %w", err)
	}
	if existingExport != nil {
		return c.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("export in progress (id: %s)", existingExport.ID),
		})
	}

	existingRestore, err := h.db.RestoreJob.Query().
		Where(restorejob.StatusIn(restorejob.StatusPending, restorejob.StatusRunning)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("check restore jobs: %w", err)
	}
	if existingRestore != nil {
		return c.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("restore already in progress (id: %s)", existingRestore.ID),
			"id":    existingRestore.ID.String(),
		})
	}

	bk, err := h.db.Backup.Get(ctx, backupUUID)
	if err != nil {
		if ent.IsNotFound(err) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "backup not found"})
		}
		return fmt.Errorf("get backup: %w", err)
	}
	if bk.Status != backup.StatusCompleted {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "backup is not in completed status"})
	}
	if bk.S3Key == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "backup has no stored file"})
	}

	if bk.SchemaVersion != "" && !req.ConfirmSchemaMismatch {
		if currentVersion, err := readSchemaVersion(h.schemaPath); err == nil && currentVersion != "" && currentVersion != bk.SchemaVersion {
			return c.JSON(http.StatusConflict, map[string]any{
				"error":                fmt.Sprintf("backup schema version %s does not match current schema %s — this restore will replace the DGraph schema. Add confirmSchemaMismatch: true to proceed.", bk.SchemaVersion, currentVersion),
				"requiresConfirmation": true,
			})
		}
	}

	actor := actorFromContext(c)
	job, err := h.db.RestoreJob.Create().
		SetStatus(restorejob.StatusPending).
		SetBackupID(backupUUID).
		SetBackupKey(bk.S3Key).
		SetCreatedBy(actor).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create restore job: %w", err)
	}

	go h.runRestore(job.ID)

	return c.JSON(http.StatusAccepted, triggerResponse{
		JobID:  job.ID.String(),
		Status: string(job.Status),
	})
}

// List handles GET /api/v1/restore/jobs
//
// @Summary     List restore jobs
// @Description Returns up to 50 restore jobs ordered by most recent first.
// @Tags        backup graph
// @Produce     json
// @Success     200 {array} restoreJobResponse
// @Router      /api/v1/restore/jobs [get]
func (h *RestoreHandler) List(c echo.Context) error {
	jobs, err := h.db.RestoreJob.Query().
		Order(restorejob.ByCreatedAt(sql.OrderDesc())).
		Limit(50).
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("list restore jobs: %w", err)
	}
	if c.Request().Header.Get("HX-Request") == "true" {
		rows := make([]restoreFragRow, 0, len(jobs))
		for _, j := range jobs {
			rows = append(rows, toRestoreFragRow(j))
		}
		tmpl, err := template.ParseFiles("web/templates/orbital/partials/restore-jobs-tbody.gohtml")
		if err != nil {
			return fmt.Errorf("parse restore fragment: %w", err)
		}
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return tmpl.Execute(c.Response(), rows)
	}
	out := make([]restoreJobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toRestoreJobResponse(j))
	}
	return c.JSON(http.StatusOK, out)
}

// Status handles GET /api/v1/restore/jobs/:jobId
//
// @Summary     Get restore job status
// @Description Returns the status of a specific restore job.
// @Tags        backup graph
// @Produce     json
// @Param       jobId path string true "Restore job ID"
// @Success     200 {object} restoreJobResponse
// @Failure     404 {object} map[string]string
// @Router      /api/v1/restore/jobs/{jobId} [get]
func (h *RestoreHandler) Status(c echo.Context) error {
	id, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}
	j, err := h.db.RestoreJob.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get restore job: %w", err)
	}
	return c.JSON(http.StatusOK, toRestoreJobResponse(j))
}

func (h *RestoreHandler) runRestore(jobID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), h.restoreTimeout)
	defer cancel()

	var logBuf strings.Builder

	fail := func(step string, err error) {
		h.logger.Error("restore failed", "jobId", jobID, "step", step, "err", err)
		errStr := fmt.Sprintf("%s: %v", step, err)
		if _, saveErr := h.db.RestoreJob.UpdateOneID(jobID).
			SetStatus(restorejob.StatusFailed).
			SetError(errStr).
			SetLog(logBuf.String()).
			SetCompletedAt(time.Now()).
			Save(context.Background()); saveErr != nil {
			h.logger.Error("failed to mark restore job failed", "jobId", jobID, "err", saveErr)
		}
	}

	log := func(msg string) {
		h.logger.Info(msg, "jobId", jobID)
		fmt.Fprintln(&logBuf, msg)
	}

	if _, err := h.db.RestoreJob.UpdateOneID(jobID).
		SetStatus(restorejob.StatusRunning).
		SetStartedAt(time.Now()).
		Save(ctx); err != nil {
		fail("mark running", err)
		return
	}

	job, err := h.db.RestoreJob.Get(ctx, jobID)
	if err != nil {
		fail("load job", err)
		return
	}
	if job.BackupID == nil {
		fail("check backup_id", fmt.Errorf("backup_id is nil"))
		return
	}

	bk, err := h.db.Backup.Get(ctx, *job.BackupID)
	if err != nil {
		fail("load backup", err)
		return
	}
	if bk.S3Key == "" {
		fail("check s3_key", fmt.Errorf("backup has no s3_key"))
		return
	}
	if bk.SchemaVersion != "" {
		log(fmt.Sprintf("Backup schema version: %s", bk.SchemaVersion))
	}

	tmpDir, err := os.MkdirTemp("", "orbital-restore-*")
	if err != nil {
		fail("create temp dir", err)
		return
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, "backup.zip")

	// Download backup zip to temp dir
	log("Downloading backup from storage...")
	if err := h.downloadToFile(ctx, bk.S3Key, zipPath); err != nil {
		fail("download backup", err)
		return
	}

	// Extract all three files from zip into temp dir
	log("Extracting backup...")
	if err := extractBackupZip(zipPath, tmpDir); err != nil {
		fail("extract backup", err)
		return
	}
	os.Remove(zipPath)
	log("Extraction complete.")

	// Drop all existing data — point of no return
	log("Dropping existing graph data...")
	if err := h.dropAll(ctx); err != nil {
		fail("drop_all", err)
		return
	}

	// Run dgraph live with the DQL schema — loads data with correct predicate types.
	log("Running dgraph live...")
	out, err := h.backend.RunLive(ctx, tmpDir, h.dgraphAlphaGRPC, h.dgraphZeroGRPC)
	fmt.Fprintln(&logBuf, out)
	if err != nil {
		fail("dgraph live", err)
		return
	}

	// Re-apply the GraphQL schema — drop_all wiped it and dgraph live only restores DQL predicates.
	log("Applying GraphQL schema...")
	if err := h.applyBlueSchema(ctx); err != nil {
		fail("apply schema", err)
		return
	}

	log("Restore completed.")

	// Attach every DataCenter in the restored graph to the audit event so that
	// resource-scoped audit panels (DC, Server) show the restore as relevant
	// context. Restore wipes + reloads DGraph, so by construction every DC in
	// the restored snapshot was "affected" by this operation.
	dcOrbIDs := h.fetchAllDCOrbIDs(ctx)
	var resourceTypes []string
	if len(dcOrbIDs) > 0 {
		resourceTypes = []string{"DataCenter"}
	}
	writeAuditEvent(h.db, h.logger, "management", job.CreatedBy, "restoreBackup",
		[]string{"restoreBackup"},
		resourceTypes,
		dcOrbIDs,
		map[string]any{"id": jobID.String(), "backupKey": bk.S3Key},
	)
	if _, err := h.db.RestoreJob.UpdateOneID(jobID).
		SetStatus(restorejob.StatusCompleted).
		SetLog(logBuf.String()).
		SetCompletedAt(time.Now()).
		Save(context.Background()); err != nil {
		h.logger.Error("failed to mark restore job completed", "jobId", jobID, "err", err)
	}
}

// downloadToFile downloads the S3 object at s3Key to destPath on disk via a presigned URL.
func (h *RestoreHandler) downloadToFile(ctx context.Context, s3Key, destPath string) error {
	presignedURL, err := h.storage.PresignURL(ctx, s3Key, presignTTL)
	if err != nil {
		return fmt.Errorf("presign url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, presignedURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: unexpected status %d", resp.StatusCode)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// extractBackupZip extracts data.json.gz, schema.gz, and gql_schema.gz from zipPath into destDir.
func extractBackupZip(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	extracted := map[string]bool{}
	for _, f := range zr.File {
		switch f.Name {
		case "data.json.gz", "schema.gz", "gql_schema.gz":
		default:
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		out, err := os.Create(filepath.Join(destDir, f.Name))
		if err != nil {
			rc.Close()
			return fmt.Errorf("create %s: %w", f.Name, err)
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return fmt.Errorf("extract %s: %w", f.Name, err)
		}
		extracted[f.Name] = true
	}

	if !extracted["data.json.gz"] {
		return fmt.Errorf("data.json.gz not found in backup zip")
	}
	if !extracted["schema.gz"] {
		return fmt.Errorf("schema.gz not found in backup zip")
	}
	if !extracted["gql_schema.gz"] {
		return fmt.Errorf("gql_schema.gz not found in backup zip")
	}
	return nil
}

// dropAll calls DGraph's /alter endpoint with drop_all: true.
func (h *RestoreHandler) dropAll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphAlterURL,
		strings.NewReader(`{"drop_all": true}`))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("alter request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("alter returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (h *RestoreHandler) applyBlueSchema(ctx context.Context) error {
	schemaBytes, err := os.ReadFile(h.schemaPath)
	if err != nil {
		return fmt.Errorf("read schema file: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphSchemaURL, bytes.NewReader(schemaBytes))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("schema apply request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("schema apply returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ── Fragment renderer ─────────────────────────────────────────────────────────

type restoreFragRow struct {
	StartedAt   string
	Status      string
	StatusClass string
	BackupLabel string
	CreatedBy   string
	Duration    string
	HasLog      bool
	Log         string
	Error       string
}

func toRestoreFragRow(j *ent.RestoreJob) restoreFragRow {
	statusClass := map[string]string{
		"completed": "is-success",
		"failed":    "is-danger",
		"running":   "is-warning",
	}[string(j.Status)]
	if statusClass == "" {
		statusClass = "is-light"
	}

	startedAt := "—"
	if j.StartedAt != nil {
		startedAt = j.StartedAt.UTC().Format("2006-01-02 15:04:05")
	} else {
		startedAt = j.CreatedAt.UTC().Format("2006-01-02 15:04:05")
	}

	duration := "—"
	switch {
	case j.StartedAt != nil && j.CompletedAt != nil:
		secs := int(j.CompletedAt.Sub(*j.StartedAt).Seconds())
		duration = fmt.Sprintf("%ds", secs)
	case string(j.Status) == "running":
		duration = "Running..."
	}

	backupLabel := "—"
	if j.BackupKey != nil {
		parts := strings.Split(*j.BackupKey, "/")
		backupLabel = parts[len(parts)-1]
	} else if j.BackupID != nil {
		id := j.BackupID.String()
		if len(id) > 8 {
			backupLabel = id[:8] + "..."
		} else {
			backupLabel = id
		}
	}

	row := restoreFragRow{
		StartedAt:   startedAt,
		Status:      string(j.Status),
		StatusClass: statusClass,
		BackupLabel: backupLabel,
		CreatedBy:   j.CreatedBy,
		Duration:    duration,
		HasLog:      j.Log != nil || j.Error != nil,
	}
	if j.Log != nil {
		row.Log = *j.Log
	}
	if j.Error != nil {
		row.Error = *j.Error
	}
	return row
}

// ListRows handles GET /api/v1/restore/jobs/rows
// Returns an HTML fragment containing the restore jobs tbody rows.


