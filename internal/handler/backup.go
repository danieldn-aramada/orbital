package handler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/restorejob"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	cron "github.com/robfig/cron/v3"
)

const presignTTL = 15 * time.Minute

// blobStorage abstracts upload/download/delete over S3-compatible and Azure Blob backends.
type blobStorage interface {
	upload(ctx context.Context, localPath, key string) error
	presignURL(ctx context.Context, key string) (string, error)
	deleteObject(ctx context.Context, key string) error
	ping(ctx context.Context) error
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
	storage                  blobStorage
	s3Bucket                 string
	s3Prefix                 string
	s3Endpoint               string
	retentionDays            int
	retentionMinCount        int
	bootstrapCronSpec        string // value of ORBITAL_BACKUP_SCHEDULE; cron expression to seed first schedule row
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
	BootstrapCronSpec        string  // cron expression; seeds the backup_schedules row if none exists; e.g. "0 8 * * *" = midnight PT
	RawDB                    *sql.DB // optional: underlying *sql.DB for pg_try_advisory_xact_lock
	Version                  string
}

func NewBackupHandler(ctx context.Context, db *ent.Client, cfg BackupConfig, logger *slog.Logger) (*BackupHandler, error) {
	var store blobStorage
	var err error

	if strings.Contains(cfg.S3Endpoint, ".blob.core.windows.net") {
		store, err = newAzureStorage(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
	} else {
		store, err = newS3Storage(ctx, cfg.S3Endpoint, cfg.S3Region, cfg.S3Bucket, cfg.S3AccessKey, cfg.S3SecretKey)
	}
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
		bootstrapCronSpec:        cfg.BootstrapCronSpec,
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

// cronParser is the robfig/cron parser used for validation and scheduling.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// BootstrapSchedule creates the initial backup_schedules row from env vars
// (ORBITAL_BACKUP_SCHEDULE, ORBITAL_BACKUP_SCHEDULE_TZ) if no row exists yet.
// Once a row is in the DB, env vars are ignored.
func (h *BackupHandler) BootstrapSchedule(ctx context.Context) error {
	if h.db == nil || h.bootstrapCronSpec == "" {
		return nil
	}
	_, err := h.db.BackupSchedule.Query().First(ctx)
	if err == nil {
		return nil // row exists; DB owns the schedule
	}
	if !ent.IsNotFound(err) {
		return fmt.Errorf("bootstrap schedule query: %w", err)
	}
	if _, err := cronParser.Parse(h.bootstrapCronSpec); err != nil {
		return fmt.Errorf("invalid ORBITAL_BACKUP_SCHEDULE %q: %w", h.bootstrapCronSpec, err)
	}
	_, err = h.db.BackupSchedule.Create().
		SetCronSpec(h.bootstrapCronSpec).
		SetTimezone("UTC").
		SetEnabled(true).
		SetCreatedBy("system").
		SetUpdatedBy("system").
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create backup schedule: %w", err)
	}
	h.logger.Info("bootstrapped backup schedule from env", "spec", h.bootstrapCronSpec)
	return nil
}

// schedLocation parses the schedule's timezone string into a *time.Location.
// Falls back to UTC on error.
func schedLocation(sched *ent.BackupSchedule) *time.Location {
	if sched.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// buildCronSpec returns the cron spec and location for the schedule.
func buildCronSpec(sched *ent.BackupSchedule) (spec string, loc *time.Location) {
	return sched.CronSpec, schedLocation(sched)
}

// isMissedRun returns true when the schedule is enabled and a scheduled run
// should have occurred since last_triggered_at but didn't (e.g. downtime).
func isMissedRun(sched *ent.BackupSchedule) bool {
	if !sched.Enabled {
		return false
	}
	schedule, err := cronParser.Parse(sched.CronSpec)
	if err != nil {
		return false
	}
	ref := sched.CreatedAt
	if sched.LastTriggeredAt != nil {
		ref = *sched.LastTriggeredAt
	}
	return schedule.Next(ref).Before(time.Now())
}

// fireCatchUp fires a backup immediately if a run was missed during downtime.
func (h *BackupHandler) fireCatchUp(ctx context.Context) {
	sched, err := h.db.BackupSchedule.Query().First(ctx)
	if ent.IsNotFound(err) || err != nil {
		return
	}
	if isMissedRun(sched) {
		h.logger.Info("scheduler: catch-up firing missed backup run")
		h.fire(ctx)
	}
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

	schedule, err := h.db.BackupSchedule.Query().First(ctx)
	if ent.IsNotFound(err) {
		return
	}
	if err != nil {
		h.logger.Warn("scheduler: error reading schedule", "err", err)
		return
	}
	if !schedule.Enabled {
		return
	}

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

	// Update last_triggered_at BEFORE launching the goroutine so a crash
	// during the backup doesn't cause a re-trigger on restart.
	_, err = h.db.BackupSchedule.UpdateOne(schedule).
		SetLastTriggeredAt(time.Now().UTC()).
		SetUpdatedBy("scheduler").
		Save(ctx)
	if err != nil {
		h.logger.Warn("scheduler: failed to update last_triggered_at", "err", err)
	}

	go h.runBackup(job.ID)

	writeAuditEvent(h.db, h.logger, "management", "scheduler", "createBackup",
		[]string{"createBackup"}, nil, nil,
		map[string]any{"jobId": job.ID.String(), "trigger": "scheduled"},
	)
	h.logger.Info("scheduled backup triggered", "jobId", job.ID)
}

// restartCron stops any running cron, reads the current schedule from the DB,
// and starts a new cron if the schedule is enabled. Safe to call from any goroutine.
func (h *BackupHandler) restartCron(ctx context.Context) {
	h.cronMu.Lock()
	defer h.cronMu.Unlock()

	if h.cronJob != nil {
		h.cronJob.Stop()
		h.cronJob = nil
	}

	if h.db == nil {
		return
	}
	sched, err := h.db.BackupSchedule.Query().First(ctx)
	if err != nil || !sched.Enabled {
		return
	}

	spec, loc := buildCronSpec(sched)
	c := cron.New(cron.WithLocation(loc))
	if _, err := c.AddFunc(spec, func() { h.fire(context.Background()) }); err != nil {
		h.logger.Warn("scheduler: invalid cron spec", "spec", spec, "err", err)
		return
	}
	c.Start()
	h.cronJob = c
	h.logger.Info("backup scheduler started", "spec", spec, "tz", sched.Timezone)
}

// StartScheduler bootstraps the schedule, fires any missed run, then runs the
// cron scheduler until ctx is cancelled. Call as a goroutine.
func (h *BackupHandler) StartScheduler(ctx context.Context) {
	if h.db == nil {
		return
	}
	if err := h.BootstrapSchedule(ctx); err != nil {
		h.logger.Warn("backup schedule bootstrap failed", "err", err)
	}

	// Catch-up: fire once immediately if a run was missed during downtime.
	h.fireCatchUp(ctx)

	// Start the cron scheduler.
	h.restartCron(ctx)

	<-ctx.Done()

	h.cronMu.Lock()
	if h.cronJob != nil {
		h.cronJob.Stop()
		h.cronJob = nil
	}
	h.cronMu.Unlock()
}

// ── Azure Blob Storage backend ─────────────────────────────────────────────────

type azureStorage struct {
	client    *azblob.Client
	svcClient *service.Client
	container string
	accountName string
	accountKey  string
}

func newAzureStorage(endpoint, accountName, accountKey, container string) (*azureStorage, error) {
	cred, err := azblob.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("azure shared key credential: %w", err)
	}
	client, err := azblob.NewClientWithSharedKeyCredential(endpoint, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azure blob client: %w", err)
	}
	svcCred, err := service.NewClientWithSharedKeyCredential(endpoint, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azure service client: %w", err)
	}
	return &azureStorage{client: client, svcClient: svcCred, container: container, accountName: accountName, accountKey: accountKey}, nil
}

func (a *azureStorage) upload(ctx context.Context, localPath, key string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = a.client.UploadFile(ctx, a.container, key, f, nil)
	return err
}

func (a *azureStorage) presignURL(ctx context.Context, key string) (string, error) {
	cred, err := azblob.NewSharedKeyCredential(a.accountName, a.accountKey)
	if err != nil {
		return "", err
	}
	sasQueryParams, err := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPS,
		StartTime:     time.Now().UTC(),
		ExpiryTime:    time.Now().UTC().Add(presignTTL),
		Permissions:   to(sas.BlobPermissions{Read: true}).String(),
		ContainerName: a.container,
		BlobName:      key,
	}.SignWithSharedKey(cred)
	if err != nil {
		return "", err
	}
	blobURL := fmt.Sprintf("%s/%s/%s?%s", a.svcClient.URL(), a.container, key, sasQueryParams.Encode())
	return blobURL, nil
}

func (a *azureStorage) deleteObject(ctx context.Context, key string) error {
	_, err := a.client.DeleteBlob(ctx, a.container, key, nil)
	return err
}

func (a *azureStorage) ping(ctx context.Context) error {
	pager := a.client.NewListBlobsFlatPager(a.container, nil)
	_, err := pager.NextPage(ctx)
	return err
}

func to[T any](v T) *T { return &v }

// ── S3-compatible backend ──────────────────────────────────────────────────────

type s3Storage struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

func newS3Storage(ctx context.Context, endpoint, region, bucket, accessKey, secretKey string) (*s3Storage, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	opts := []func(*s3.Options){}
	if endpoint != "" {
		ep := endpoint
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = &ep
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, opts...)
	return &s3Storage{client: client, presign: s3.NewPresignClient(client), bucket: bucket}, nil
}

func (s *s3Storage) upload(ctx context.Context, localPath, key string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
		Body:   f,
	})
	return err
}

func (s *s3Storage) presignURL(ctx context.Context, key string) (string, error) {
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(presignTTL))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *s3Storage) deleteObject(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	return err
}

func (s *s3Storage) ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	return err
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

// TestConnection handles POST /api/v1/backup/test-connection
func (h *BackupHandler) TestConnection(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
	defer cancel()
	if err := h.storage.ping(ctx); err != nil {
		return c.JSON(http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
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
		map[string]any{"jobId": job.ID.String(), "trigger": string(triggerVal)},
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

	url, err := h.storage.presignURL(c.Request().Context(), j.S3Key)
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
		if err := h.storage.deleteObject(c.Request().Context(), j.S3Key); err != nil {
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

	ts := time.Now().UTC().Format("20060102T150405Z")
	zipName := fmt.Sprintf("orbital-%s-%s.zip", h.version, ts)
	zipPath := filepath.Join(os.TempDir(), zipName)
	if err := writeZip(zipPath, dataGZ, dqlSchemaGZ, gqlSchemaGZ); err != nil {
		return fmt.Errorf("write zip: %w", err)
	}
	defer os.Remove(zipPath)

	storageKey := fmt.Sprintf("%s%s", h.s3Prefix, zipName)
	log.Info("uploading backup", "bucket", h.s3Bucket, "key", storageKey)
	if err := h.storage.upload(ctx, zipPath, storageKey); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	log.Info("upload complete")

	zipInfo, _ := os.Stat(zipPath)
	var sizeBytes int64
	if zipInfo != nil {
		sizeBytes = zipInfo.Size()
	}

	_, err = h.db.Backup.UpdateOneID(jobID).
		SetStatus(backup.StatusCompleted).
		SetS3Bucket(h.s3Bucket).
		SetS3Key(storageKey).
		SetS3Endpoint(h.s3Endpoint).
		SetChecksum(checksum).
		SetSizeBytes(sizeBytes).
		SetCompletedAt(time.Now()).
		Save(ctx)
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
			if err := h.storage.deleteObject(ctx, old.S3Key); err != nil {
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

// ── Schedule REST endpoints ────────────────────────────────────────────────────

type scheduleResponse struct {
	CronSpec        string `json:"cronSpec"`
	Timezone        string `json:"timezone"`
	Enabled         bool   `json:"enabled"`
	LastTriggeredAt string `json:"lastTriggeredAt,omitempty"`
	NextRunApprox   string `json:"nextRunApprox,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

func toScheduleResponse(s *ent.BackupSchedule) scheduleResponse {
	r := scheduleResponse{
		CronSpec:  s.CronSpec,
		Timezone:  s.Timezone,
		Enabled:   s.Enabled,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
	}
	if s.UpdatedAt != nil && !s.UpdatedAt.IsZero() {
		r.UpdatedAt = s.UpdatedAt.Format(time.RFC3339)
	}
	if s.LastTriggeredAt != nil {
		r.LastTriggeredAt = s.LastTriggeredAt.Format(time.RFC3339)
	}
	spec, loc := buildCronSpec(s)
	if parsed, err := cronParser.Parse(spec); err == nil {
		r.NextRunApprox = parsed.Next(time.Now().In(loc)).Format(time.RFC3339)
	}
	return r
}

// GetSchedule handles GET /api/v1/backup/schedule
func (h *BackupHandler) GetSchedule(c echo.Context) error {
	s, err := h.db.BackupSchedule.Query().First(c.Request().Context())
	if ent.IsNotFound(err) {
		return c.JSON(http.StatusOK, nil)
	}
	if err != nil {
		return fmt.Errorf("get schedule: %w", err)
	}
	return c.JSON(http.StatusOK, toScheduleResponse(s))
}

// UpdateSchedule handles PUT /api/v1/backup/schedule
func (h *BackupHandler) UpdateSchedule(c echo.Context) error {
	var body struct {
		CronSpec *string `json:"cronSpec"`
		Timezone *string `json:"timezone"`
		Enabled  *bool   `json:"enabled"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.ErrBadRequest
	}

	// Validate cron spec if provided.
	if body.CronSpec != nil {
		if _, err := cronParser.Parse(*body.CronSpec); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid cron expression: " + err.Error()})
		}
	}
	// Validate timezone if provided.
	if body.Timezone != nil && *body.Timezone != "" {
		if _, err := time.LoadLocation(*body.Timezone); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid timezone: " + *body.Timezone})
		}
	}

	ctx := c.Request().Context()
	actor := actorFromContext(c)

	existing, err := h.db.BackupSchedule.Query().First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query schedule: %w", err)
	}

	if existing == nil {
		if body.CronSpec == nil || *body.CronSpec == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "cronSpec is required"})
		}
		create := h.db.BackupSchedule.Create().
			SetCronSpec(*body.CronSpec).
			SetCreatedBy(actor).
			SetUpdatedBy(actor)
		if body.Timezone != nil {
			create = create.SetTimezone(*body.Timezone)
		}
		if body.Enabled != nil {
			create = create.SetEnabled(*body.Enabled)
		}
		s, err := create.Save(ctx)
		if err != nil {
			return fmt.Errorf("create schedule: %w", err)
		}
		writeAuditEvent(h.db, h.logger, "management", actor, "updateBackupSchedule",
			[]string{"updateBackupSchedule"}, nil, nil,
			map[string]any{"cronSpec": s.CronSpec, "enabled": s.Enabled, "timezone": s.Timezone},
		)
		h.restartCron(ctx)
		return c.JSON(http.StatusOK, toScheduleResponse(s))
	}

	// Update existing row — only fields present in the request body are changed.
	update := h.db.BackupSchedule.UpdateOne(existing).SetUpdatedBy(actor)
	if body.CronSpec != nil {
		update = update.SetCronSpec(*body.CronSpec)
	}
	if body.Timezone != nil {
		update = update.SetTimezone(*body.Timezone)
	}
	if body.Enabled != nil {
		update = update.SetEnabled(*body.Enabled)
	}
	s, err := update.Save(ctx)
	if err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	writeAuditEvent(h.db, h.logger, "management", actor, "updateBackupSchedule",
		[]string{"updateBackupSchedule"}, nil, nil,
		map[string]any{"cronSpec": s.CronSpec, "enabled": s.Enabled, "timezone": s.Timezone},
	)
	h.restartCron(ctx)
	return c.JSON(http.StatusOK, toScheduleResponse(s))
}
