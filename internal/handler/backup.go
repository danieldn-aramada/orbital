package handler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/restorejob"
	"github.com/armada/orbital/internal/blobstore"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	cron "github.com/robfig/cron/v3"
)

const presignTTL = 15 * time.Minute

type backupManifest struct {
	ManifestVersion int    `json:"manifestVersion"`
	CreatedAt       string `json:"createdAt"`
	OrbitalVersion  string `json:"orbitalVersion"`
	SchemaVersion   string `json:"schemaVersion"`
}

// readSchemaVersion reads the schema/VERSION file adjacent to schemaPath.
func readSchemaVersion(schemaPath string) (string, error) {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(schemaPath), "VERSION"))
	if err != nil {
		return "", fmt.Errorf("read schema/VERSION: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// schedulerAdvisoryLockKey is the PostgreSQL advisory lock key for the backup scheduler.
// Transaction-scoped: auto-releases if the process crashes.
const schedulerAdvisoryLockKey int64 = 5555100001

// BackupHandler handles async DGraph backup operations.
type BackupHandler struct {
	db                       *ent.Client
	rawDB                    *sql.DB // underlying *sql.DB for advisory locks; nil skips locking
	dgraphAdminURL           string
	dgraphExportDir          string
	dgraphContainerExportDir string
	schemaPath               string
	storage                  blobstore.Store
	s3Bucket                 string
	s3Prefix                 string
	s3Endpoint               string
	retentionDays            int
	retentionMinCount        int
	cronSpec                 string // value of ORBITAL_BACKUP_SCHEDULE; empty = scheduler disabled
	version                  string
	logger                   *slog.Logger
	cronJob                  *cron.Cron // nil when scheduler is stopped or disabled
	cronMu                   sync.Mutex
}

// BackupConfig holds all storage and DGraph configuration for the backup handler.
type BackupConfig struct {
	DGraphAdminURL           string
	DGraphExportDir          string // host-side path mounted to DGraphContainerExportDir inside DGraph
	DGraphContainerExportDir string // container-side export path; defaults to /dgraph/export
	SchemaPath               string
	S3Endpoint               string
	S3Region                 string
	S3Bucket                 string
	S3AccessKey              string
	S3SecretKey              string
	S3Prefix                 string
	RetentionDays            int     // delete backups older than N days; 0 = no time-based pruning
	RetentionMinCount        int     // always keep at least N backups regardless of age
	CronSpec                 string  // cron expression for scheduler; empty = disabled; e.g. "0 8 * * *" = 08:00 UTC daily
	RawDB                    *sql.DB // optional: underlying *sql.DB for pg_try_advisory_xact_lock
	Version                  string
}

func NewBackupHandler(ctx context.Context, db *ent.Client, cfg BackupConfig, logger *slog.Logger) (*BackupHandler, error) {
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

	prefix := cfg.S3Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	containerExportDir := cfg.DGraphContainerExportDir
	if containerExportDir == "" {
		containerExportDir = "/dgraph/export"
	}

	return &BackupHandler{
		db:                       db,
		rawDB:                    cfg.RawDB,
		dgraphAdminURL:           cfg.DGraphAdminURL,
		dgraphExportDir:          cfg.DGraphExportDir,
		dgraphContainerExportDir: containerExportDir,
		schemaPath:               cfg.SchemaPath,
		storage:                  store,
		s3Bucket:                 cfg.S3Bucket,
		s3Prefix:                 prefix,
		s3Endpoint:               cfg.S3Endpoint,
		retentionDays:            cfg.RetentionDays,
		retentionMinCount:        cfg.RetentionMinCount,
		cronSpec:                 cfg.CronSpec,
		version:                  cfg.Version,
		logger:                   logger,
	}, nil
}

// ── Scheduler ─────────────────────────────────────────────────────────────────

// tryAdvisoryLock attempts a PostgreSQL transaction-scoped advisory lock.
// Returns (acquired=true, unlock) or (acquired=false, nil, nil).
// The unlock func rolls back the transaction, releasing the lock.
// Using pg_try_advisory_xact_lock: auto-releases on transaction end (crash-safe).
// If rawDB is nil, locking is skipped and acquired=true is returned (single-replica safe).
func tryAdvisoryLock(ctx context.Context, rawDB *sql.DB) (bool, func(), error) {
	if rawDB == nil {
		return true, func() {}, nil
	}
	tx, err := rawDB.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, fmt.Errorf("advisory lock begin tx: %w", err)
	}
	var acquired bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", schedulerAdvisoryLockKey).Scan(&acquired); err != nil {
		tx.Rollback() //nolint:errcheck
		return false, nil, fmt.Errorf("advisory lock query: %w", err)
	}
	if !acquired {
		tx.Rollback() //nolint:errcheck
		return false, nil, nil
	}
	return true, func() { tx.Rollback() }, nil //nolint:errcheck
}

// cronParser is the robfig/cron parser used for validation.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// isMissedRun returns true if a scheduled run should have occurred since the
// last scheduled backup job but didn't (e.g. orbital was down).
func (h *BackupHandler) isMissedRun(ctx context.Context) bool {
	if h.cronSpec == "" {
		return false
	}
	schedule, err := cronParser.Parse(h.cronSpec)
	if err != nil {
		return false
	}
	last, err := h.db.Backup.Query().
		Where(backup.TriggerEQ(backup.TriggerScheduled)).
		Order(ent.Desc(backup.FieldCreatedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		return false // no prior scheduled run; don't catch-up on first boot
	}
	if err != nil {
		return false
	}
	return schedule.Next(last.CreatedAt).Before(time.Now())
}

// fire creates a scheduled backup job and launches it. Acquires the advisory
// lock so concurrent replicas don't double-fire.
func (h *BackupHandler) fire(ctx context.Context) {
	acquired, unlock, err := tryAdvisoryLock(ctx, h.rawDB)
	if err != nil {
		h.logger.Warn("scheduler advisory lock error", "err", err)
		return
	}
	if !acquired {
		return // another replica is handling this
	}
	defer unlock()

	// Don't stack scheduled backups on top of an already-running one.
	existing, err := h.db.Backup.Query().
		Where(backup.StatusIn(backup.StatusPending, backup.StatusRunning)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		h.logger.Warn("scheduler: error checking in-progress backups", "err", err)
		return
	}
	if existing != nil {
		h.logger.Info("scheduler: backup already in progress, skipping", "existingId", existing.ID)
		return
	}

	job, err := h.db.Backup.Create().
		SetStatus(backup.StatusPending).
		SetTrigger(backup.TriggerScheduled).
		SetCreatedBy("scheduler").
		Save(ctx)
	if err != nil {
		h.logger.Error("scheduler: failed to create backup job", "err", err)
		return
	}

	go h.runBackup(job.ID)

	writeAuditEvent(h.db, h.logger, "management", "scheduler", "createBackup",
		[]string{"createBackup"}, nil, nil,
		map[string]any{"id": job.ID.String(), "trigger": "scheduled"},
	)
	h.logger.Info("scheduled backup triggered", "jobId", job.ID)
}

// StartScheduler fires any missed run, then runs the cron scheduler until ctx
// is cancelled. Call as a goroutine. No-ops if ORBITAL_BACKUP_SCHEDULE is empty.
func (h *BackupHandler) StartScheduler(ctx context.Context) {
	if h.db == nil || h.cronSpec == "" {
		return
	}

	if _, err := cronParser.Parse(h.cronSpec); err != nil {
		h.logger.Error("invalid ORBITAL_BACKUP_SCHEDULE — scheduler disabled", "spec", h.cronSpec, "err", err)
		return
	}

	if h.isMissedRun(ctx) {
		h.logger.Info("scheduler: catch-up firing missed backup run")
		h.fire(ctx)
	}

	h.cronMu.Lock()
	c := cron.New(cron.WithLocation(time.UTC))
	if _, err := c.AddFunc(h.cronSpec, func() { h.fire(context.Background()) }); err != nil {
		h.logger.Warn("scheduler: failed to add cron func", "spec", h.cronSpec, "err", err)
		h.cronMu.Unlock()
		return
	}
	c.Start()
	h.cronJob = c
	h.cronMu.Unlock()
	h.logger.Info("backup scheduler started", "spec", h.cronSpec)

	<-ctx.Done()

	h.cronMu.Lock()
	if h.cronJob != nil {
		h.cronJob.Stop()
		h.cronJob = nil
	}
	h.cronMu.Unlock()
}

// ── HTTP handlers ──────────────────────────────────────────────────────────────

type backupResponse struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	Trigger     string  `json:"trigger,omitempty"`
	InitiatedBy string  `json:"initiatedBy,omitempty"`
	InitiatedAt string  `json:"initiatedAt"`
	CompletedAt *string `json:"completedAt,omitempty"`
	S3Key       string  `json:"s3Key,omitempty"`
	Checksum    string  `json:"checksum,omitempty"`
	SizeBytes   *int64  `json:"sizeBytes,omitempty"`
	Error       *string `json:"error,omitempty"`
}

// TestConnection handles POST /api/v1/backup/test-connection.
//
// Content negotiation: HTMX callers (HX-Request: true) get a single-span HTML
// fragment ready to swap into a result slot. Other callers get JSON. The
// fragment shape is the same on both backup and divergence pages — both read
// from the same S3 storage backend, so one endpoint serves both UIs.
func (h *BackupHandler) TestConnection(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
	defer cancel()
	err := h.storage.Ping(ctx)
	if c.Request().Header.Get("HX-Request") == "true" {
		return renderTestConnectionFragment(c, err)
	}
	if err != nil {
		return c.JSON(http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// renderTestConnectionFragment writes the inline HTML span shown next to a
// "Test Connection" button. Kept inline (not a template file) because it's a
// single element and the markup is trivial.
func renderTestConnectionFragment(c echo.Context, pingErr error) error {
	if pingErr != nil {
		return c.HTML(http.StatusOK, `<span class="has-text-danger"><i class="fa-solid fa-circle-xmark"></i> `+template.HTMLEscapeString(pingErr.Error())+`</span>`)
	}
	return c.HTML(http.StatusOK, `<span class="has-text-success"><i class="fa-solid fa-circle-check"></i> Connected</span>`)
}

// Trigger handles POST /api/v1/backup
//
// @Summary     Trigger backup
// @Description Triggers an async DGraph backup to configured S3-compatible or Azure Blob storage. Returns immediately with a job ID. Returns 409 if a backup is already in progress.
// @Tags        backup graph
// @Produce     json
// @Success     202 {object} triggerResponse
// @Failure     409 {object} map[string]string
// @Router      /api/v1/backup [post]
func (h *BackupHandler) Trigger(c echo.Context) error {
	existing, err := h.db.Backup.Query().
		Where(backup.StatusIn(backup.StatusPending, backup.StatusRunning)).
		First(c.Request().Context())
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("check existing backup: %w", err)
	}
	if existing != nil {
		return c.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("backup already in progress (id: %s)", existing.ID),
			"id":    existing.ID.String(),
		})
	}

	existingRestore, err := h.db.RestoreJob.Query().
		Where(restorejob.StatusIn(restorejob.StatusPending, restorejob.StatusRunning)).
		First(c.Request().Context())
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("check restore jobs: %w", err)
	}
	if existingRestore != nil {
		return c.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("restore in progress (id: %s)", existingRestore.ID),
		})
	}

	initiatedBy := actorFromContext(c)

	// Optional body: {"trigger":"scheduled"} — defaults to "manual".
	var body struct {
		Trigger string `json:"trigger"`
	}
	_ = c.Bind(&body)
	triggerVal := backup.TriggerManual
	if body.Trigger == string(backup.TriggerScheduled) {
		triggerVal = backup.TriggerScheduled
	}

	job, err := h.db.Backup.Create().
		SetStatus(backup.StatusPending).
		SetTrigger(triggerVal).
		SetCreatedBy(initiatedBy).
		Save(c.Request().Context())
	if err != nil {
		return fmt.Errorf("create backup job: %w", err)
	}

	go h.runBackup(job.ID)

	writeAuditEvent(h.db, h.logger, "management", initiatedBy, "createBackup",
		[]string{"createBackup"},
		nil,
		nil,
		map[string]any{"id": job.ID.String(), "trigger": string(triggerVal)},
	)

	return c.JSON(http.StatusAccepted, triggerResponse{
		JobID:  job.ID.String(),
		Status: string(job.Status),
	})
}

// List handles GET /api/v1/backup/jobs
//
// @Summary     List backups
// @Description Returns up to 50 backup records ordered by most recent first.
// @Tags        backup graph
// @Produce     json
// @Success     200 {array}  backupResponse
// @Router      /api/v1/backup/jobs [get]
func (h *BackupHandler) List(c echo.Context) error {
	jobs, err := h.db.Backup.Query().
		Order(backup.ByCreatedAt(entsql.OrderDesc())).
		Limit(50).
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	if c.Request().Header.Get("HX-Request") == "true" {
		rows := make([]backupFragRow, 0, len(jobs))
		for _, j := range jobs {
			rows = append(rows, toBackupFragRow(j))
		}
		tmpl, err := template.ParseFiles("web/templates/orbital/partials/backup-jobs-tbody.gohtml")
		if err != nil {
			return fmt.Errorf("parse backup fragment: %w", err)
		}
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return tmpl.Execute(c.Response(), rows)
	}
	out := make([]backupResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toBackupResponse(j))
	}
	return c.JSON(http.StatusOK, out)
}

// Status handles GET /api/v1/backup/jobs/:jobId
//
// @Summary     Get backup status
// @Description Returns the current status and metadata for a single backup job.
// @Tags        backup graph
// @Produce     json
// @Param       jobId path string true "Backup job ID"
// @Success     200 {object} backupResponse
// @Failure     404 {object} map[string]string
// @Router      /api/v1/backup/jobs/{jobId} [get]
func (h *BackupHandler) Status(c echo.Context) error {
	id, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}
	j, err := h.db.Backup.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get backup: %w", err)
	}
	return c.JSON(http.StatusOK, toBackupResponse(j))
}

// Download handles GET /api/v1/backup/jobs/:jobId/download
//
// @Summary     Download backup
// @Description Returns a presigned URL (valid 15 minutes) to download the completed backup archive. Returns 404 if the job is not completed or has no archive.
// @Tags        backup graph
// @Produce     json
// @Param       jobId path string true "Backup job ID"
// @Success     200 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/v1/backup/jobs/{jobId}/download [get]
func (h *BackupHandler) Download(c echo.Context) error {
	id, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}
	j, err := h.db.Backup.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get backup: %w", err)
	}
	if j.Status != backup.StatusCompleted || j.S3Key == "" {
		return echo.ErrNotFound
	}

	url, err := h.storage.PresignURL(c.Request().Context(), j.S3Key, presignTTL)
	if err != nil {
		return fmt.Errorf("presign: %w", err)
	}
	return c.JSON(http.StatusOK, map[string]string{
		"url":       url,
		"expiresIn": presignTTL.String(),
	})
}

// Delete handles DELETE /api/v1/backup/jobs/:jobId
//
// @Summary     Delete backup
// @Description Deletes the backup record and its archive from storage. Returns 409 if the backup is still running.
// @Tags        backup graph
// @Produce     json
// @Param       jobId path string true "Backup job ID"
// @Success     204
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /api/v1/backup/jobs/{jobId} [delete]
func (h *BackupHandler) Delete(c echo.Context) error {
	id, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}
	j, err := h.db.Backup.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get backup: %w", err)
	}
	if j.Status == backup.StatusRunning || j.Status == backup.StatusPending {
		return c.JSON(http.StatusConflict, map[string]string{"error": "cannot delete a backup that is in progress"})
	}
	if j.S3Key != "" {
		if err := h.storage.Delete(c.Request().Context(), j.S3Key); err != nil {
			h.logger.Warn("failed to delete backup from storage", "key", j.S3Key, "err", err)
		}
	}
	if err := h.db.Backup.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		return fmt.Errorf("delete backup record: %w", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ── Async workflow ─────────────────────────────────────────────────────────────

func (h *BackupHandler) runBackup(jobID uuid.UUID) {
	ctx := context.Background()
	log := h.logger.With("backupId", jobID)

	_, err := h.db.Backup.UpdateOneID(jobID).
		SetStatus(backup.StatusRunning).
		SetStartedAt(time.Now()).
		Save(ctx)
	if err != nil {
		log.Error("failed to mark backup running", "err", err)
		return
	}

	if err := h.doBackup(ctx, jobID, log); err != nil {
		log.Error("backup failed", "err", err)
		errStr := err.Error()
		h.db.Backup.UpdateOneID(jobID). //nolint:errcheck
						SetStatus(backup.StatusFailed).
						SetError(errStr).
						Save(ctx)
	}
}

func (h *BackupHandler) doBackup(ctx context.Context, jobID uuid.UUID, log *slog.Logger) error {
	// Use a job-specific export destination so consecutive backups never collide
	// on the same DGraph raft-index + minute combination.
	containerDest := h.dgraphContainerExportDir + "/" + jobID.String()
	hostDest := filepath.Join(h.dgraphExportDir, jobID.String())
	defer os.RemoveAll(hostDest) //nolint:errcheck

	log.Info("triggering DGraph export on blue")
	if err := h.triggerBlueExport(containerDest); err != nil {
		return fmt.Errorf("trigger blue export: %w", err)
	}

	log.Info("locating exported file")
	dataGZPath, err := h.findExport(hostDest)
	if err != nil {
		return fmt.Errorf("find export: %w", err)
	}
	log.Info("found export file", "path", dataGZPath)

	dataGZ, err := os.ReadFile(dataGZPath)
	if err != nil {
		return fmt.Errorf("read data.json.gz: %w", err)
	}

	sum := sha256.Sum256(dataGZ)
	checksum := hex.EncodeToString(sum[:])
	log.Info("computed checksum", "sha256", checksum)

	dqlSchemaGZPath, err := h.findExportFile(dataGZPath, ".schema.gz")
	if err != nil {
		return fmt.Errorf("find dql schema export: %w", err)
	}
	dqlSchemaGZ, err := os.ReadFile(dqlSchemaGZPath)
	if err != nil {
		return fmt.Errorf("read schema.gz: %w", err)
	}

	gqlSchemaGZPath, err := h.findExportFile(dataGZPath, ".gql_schema.gz")
	if err != nil {
		return fmt.Errorf("find gql schema export: %w", err)
	}
	gqlSchemaGZ, err := os.ReadFile(gqlSchemaGZPath)
	if err != nil {
		return fmt.Errorf("read gql_schema.gz: %w", err)
	}

	schemaVersion, err := readSchemaVersion(h.schemaPath)
	if err != nil {
		log.Warn("could not read schema/VERSION", "err", err)
		schemaVersion = ""
	}

	manifestJSON, _ := json.Marshal(backupManifest{
		ManifestVersion: 1,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		OrbitalVersion:  h.version,
		SchemaVersion:   schemaVersion,
	})

	ts := time.Now().UTC().Format("20060102T150405Z")
	var zipName string
	if schemaVersion != "" {
		zipName = fmt.Sprintf("orbital-schema-%s-binary-%s-%s.zip", schemaVersion, h.version, ts)
	} else {
		zipName = fmt.Sprintf("orbital-%s-%s.zip", h.version, ts)
	}
	zipPath := filepath.Join(os.TempDir(), zipName)
	if err := writeZip(zipPath, dataGZ, dqlSchemaGZ, gqlSchemaGZ, manifestJSON); err != nil {
		return fmt.Errorf("write zip: %w", err)
	}
	defer os.Remove(zipPath)

	storageKey := fmt.Sprintf("%s%s", h.s3Prefix, zipName)
	log.Info("uploading backup", "bucket", h.s3Bucket, "key", storageKey)
	zf, err := os.Open(zipPath)
	if err != nil {
		return fmt.Errorf("open backup zip for upload: %w", err)
	}
	if err := h.storage.Put(ctx, storageKey, zf, "application/zip"); err != nil {
		zf.Close()
		return fmt.Errorf("upload: %w", err)
	}
	zf.Close()
	log.Info("upload complete")

	zipInfo, _ := os.Stat(zipPath)
	var sizeBytes int64
	if zipInfo != nil {
		sizeBytes = zipInfo.Size()
	}

	u := h.db.Backup.UpdateOneID(jobID).
		SetStatus(backup.StatusCompleted).
		SetS3Bucket(h.s3Bucket).
		SetS3Key(storageKey).
		SetS3Endpoint(h.s3Endpoint).
		SetChecksum(checksum).
		SetSizeBytes(sizeBytes).
		SetBinaryVersion(h.version).
		SetCompletedAt(time.Now())
	if schemaVersion != "" {
		u = u.SetSchemaVersion(schemaVersion)
	}
	_, err = u.Save(ctx)
	if err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	if h.retentionMinCount > 0 || h.retentionDays > 0 {
		if err := h.enforceRetention(ctx, log); err != nil {
			log.Warn("retention enforcement failed", "err", err)
		}
	}
	return nil
}

func (h *BackupHandler) triggerBlueExport(destPath string) error {
	mutation := fmt.Sprintf(`{"query": "mutation { export(input: { format: \"json\", destination: \"%s\" }) { response { code message } } }"}`, destPath)
	resp, err := http.Post(h.dgraphAdminURL, "application/json", strings.NewReader(mutation))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("export mutation failed (%d): %s", resp.StatusCode, b)
	}
	return nil
}

func (h *BackupHandler) findExport(dir string) (string, error) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var found string
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error { //nolint:errcheck
			if err == nil && !info.IsDir() && strings.HasSuffix(path, ".json.gz") {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
		time.Sleep(1 * time.Second)
	}
	return "", fmt.Errorf("no json.gz found in %s after export", dir)
}

// findExportFile finds a file with the given suffix in the same directory as dataGZPath.
func (h *BackupHandler) findExportFile(dataGZPath, suffix string) (string, error) {
	dir := filepath.Dir(dataGZPath)
	var found string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error { //nolint:errcheck
		if err == nil && !info.IsDir() && strings.HasSuffix(path, suffix) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", fmt.Errorf("no *%s found in %s after export", suffix, dir)
	}
	return found, nil
}

func (h *BackupHandler) enforceRetention(ctx context.Context, log *slog.Logger) error {
	completed, err := h.db.Backup.Query().
		Where(backup.StatusEQ(backup.StatusCompleted)).
		Order(backup.ByCreatedAt(entsql.OrderDesc())).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query completed backups: %w", err)
	}

	// Determine which records are candidates for deletion:
	// - older than retentionDays (if set)
	// - but always keep at least retentionMinCount records
	cutoff := time.Time{}
	if h.retentionDays > 0 {
		cutoff = time.Now().UTC().AddDate(0, 0, -h.retentionDays)
	}

	minKeep := h.retentionMinCount
	if minKeep <= 0 {
		minKeep = 3
	}

	var toDelete []*ent.Backup
	for i, b := range completed {
		if i < minKeep {
			continue // always keep the newest minKeep
		}
		if !cutoff.IsZero() && b.CreatedAt.Before(cutoff) {
			toDelete = append(toDelete, b)
		}
	}

	for _, old := range toDelete {
		if old.S3Key != "" {
			if err := h.storage.Delete(ctx, old.S3Key); err != nil {
				log.Warn("failed to delete old backup from storage", "key", old.S3Key, "err", err)
			}
		}
		if err := h.db.Backup.DeleteOneID(old.ID).Exec(ctx); err != nil {
			log.Warn("failed to delete old backup record", "id", old.ID, "err", err)
		}
	}
	return nil
}

func toBackupResponse(j *ent.Backup) backupResponse {
	r := backupResponse{
		ID:          j.ID.String(),
		Status:      string(j.Status),
		Trigger:     string(j.Trigger),
		InitiatedBy: j.CreatedBy,
		InitiatedAt: j.CreatedAt.Format(time.RFC3339),
		S3Key:       j.S3Key,
		Checksum:    j.Checksum,
		SizeBytes:   j.SizeBytes,
		Error:       j.Error,
	}
	if j.CompletedAt != nil {
		s := j.CompletedAt.Format(time.RFC3339)
		r.CompletedAt = &s
	}
	return r
}

// ── Fragment renderer ─────────────────────────────────────────────────────────

type backupFragRow struct {
	ID            string
	InitiatedAt   string
	Status        string
	StatusClass   string
	StatusError   string
	Trigger       string
	TriggerClass  string
	InitiatedBy   string
	SizeBytes     string
	Checksum      string
	ChecksumShort string
	CanDownload   bool
	CanDelete     bool
}

func toBackupFragRow(j *ent.Backup) backupFragRow {
	statusClass := map[string]string{
		"completed": "is-success",
		"running":   "is-warning",
		"pending":   "is-warning",
		"failed":    "is-danger",
	}[string(j.Status)]
	if statusClass == "" {
		statusClass = "is-light"
	}
	triggerClass := "is-light"
	if j.Trigger == backup.TriggerScheduled {
		triggerClass = "is-info is-light"
	}
	row := backupFragRow{
		ID:          j.ID.String(),
		InitiatedAt: j.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
		Status:      string(j.Status),
		StatusClass: statusClass,
		Trigger:     string(j.Trigger),
		TriggerClass: triggerClass,
		InitiatedBy: j.CreatedBy,
		SizeBytes:   fmtBytes(j.SizeBytes),
		Checksum:    j.Checksum,
		CanDownload: j.Status == backup.StatusCompleted && j.S3Key != "",
		CanDelete:   j.Status != backup.StatusRunning && j.Status != backup.StatusPending,
	}
	if j.Error != nil {
		row.StatusError = *j.Error
	}
	if j.Checksum != "" && len(j.Checksum) > 36 {
		row.ChecksumShort = j.Checksum[:36]
	} else {
		row.ChecksumShort = j.Checksum
	}
	return row
}

func fmtBytes(n *int64) string {
	if n == nil || *n == 0 {
		return "—"
	}
	b := *n
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	if b < 1048576 {
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(b)/1048576)
}

// ListRows handles GET /api/v1/backup/jobs/rows
// Returns an HTML fragment containing the backup jobs tbody rows.
