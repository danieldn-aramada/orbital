package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
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
)

const presignTTL = 15 * time.Minute

// blobStorage abstracts upload/download/delete over S3-compatible and Azure Blob backends.
type blobStorage interface {
	upload(ctx context.Context, localPath, key string) error
	presignURL(ctx context.Context, key string) (string, error)
	deleteObject(ctx context.Context, key string) error
	ping(ctx context.Context) error
}

// BackupHandler handles async DGraph backup operations.
type BackupHandler struct {
	db              *ent.Client
	dgraphAdminURL  string
	dgraphExportDir string
	schemaPath      string
	storage         blobStorage
	s3Bucket        string
	s3Prefix        string
	s3Endpoint      string
	retentionCount  int
	version         string
	logger          *slog.Logger
}

// BackupConfig holds all storage and DGraph configuration for the backup handler.
type BackupConfig struct {
	DGraphAdminURL  string
	DGraphExportDir string
	SchemaPath      string
	S3Endpoint      string
	S3Region        string
	S3Bucket        string
	S3AccessKey     string
	S3SecretKey     string
	S3Prefix        string
	RetentionCount  int
	Version         string
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

	return &BackupHandler{
		db:              db,
		dgraphAdminURL:  cfg.DGraphAdminURL,
		dgraphExportDir: cfg.DGraphExportDir,
		schemaPath:      cfg.SchemaPath,
		storage:         store,
		s3Bucket:        cfg.S3Bucket,
		s3Prefix:        prefix,
		s3Endpoint:      cfg.S3Endpoint,
		retentionCount:  cfg.RetentionCount,
		version:         cfg.Version,
		logger:          logger,
	}, nil
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
	InitiatedBy string  `json:"initiatedBy,omitempty"`
	InitiatedAt string  `json:"initiatedAt"`
	CompletedAt *string `json:"completedAt,omitempty"`
	S3Key       string  `json:"s3Key,omitempty"`
	Checksum    string  `json:"checksum,omitempty"`
	SizeBytes   *int64  `json:"sizeBytes,omitempty"`
	Error       *string `json:"error,omitempty"`
}

// TestConnection handles POST /api/v1/backups/test-connection
func (h *BackupHandler) TestConnection(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
	defer cancel()
	if err := h.storage.ping(ctx); err != nil {
		return c.JSON(http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// Trigger handles POST /api/v1/backups
//
// @Summary     Trigger backup
// @Description Triggers an async DGraph backup to configured S3-compatible or Azure Blob storage. Returns immediately with a job ID. Returns 409 if a backup is already in progress.
// @Tags        backup graph
// @Produce     json
// @Success     202 {object} triggerResponse
// @Failure     409 {object} map[string]string
// @Router      /api/v1/backups [post]
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

	initiatedBy, _ := c.Get("user_name").(string)
	if initiatedBy == "" {
		initiatedBy, _ = c.Get("user_email").(string)
	}

	job, err := h.db.Backup.Create().
		SetStatus(backup.StatusPending).
		SetCreatedBy(initiatedBy).
		Save(c.Request().Context())
	if err != nil {
		return fmt.Errorf("create backup job: %w", err)
	}

	go h.runBackup(job.ID)

	writeAuditEvent(h.db, h.logger, initiatedBy, "triggerBackup",
		[]string{"triggerBackup"},
		nil,
		nil,
		map[string]any{"jobId": job.ID.String()},
	)

	return c.JSON(http.StatusAccepted, triggerResponse{
		JobID:  job.ID.String(),
		Status: string(job.Status),
	})
}

// List handles GET /api/v1/backups
//
// @Summary     List backups
// @Description Returns up to 50 backup records ordered by most recent first.
// @Tags        backup graph
// @Produce     json
// @Success     200 {array}  backupResponse
// @Router      /api/v1/backups [get]
func (h *BackupHandler) List(c echo.Context) error {
	jobs, err := h.db.Backup.Query().
		Order(backup.ByCreatedAt(sql.OrderDesc())).
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

// Status handles GET /api/v1/backups/:id
//
// @Summary     Get backup status
// @Description Returns the current status and metadata for a single backup job.
// @Tags        backup graph
// @Produce     json
// @Param       id path string true "Backup job ID"
// @Success     200 {object} backupResponse
// @Failure     404 {object} map[string]string
// @Router      /api/v1/backups/{id} [get]
func (h *BackupHandler) Status(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
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

// Download handles GET /api/v1/backups/:id/download
//
// @Summary     Download backup
// @Description Returns a presigned URL (valid 15 minutes) to download the completed backup archive. Returns 404 if the job is not completed or has no archive.
// @Tags        backup graph
// @Produce     json
// @Param       id path string true "Backup job ID"
// @Success     200 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/v1/backups/{id}/download [get]
func (h *BackupHandler) Download(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
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

// Delete handles DELETE /api/v1/backups/:id
//
// @Summary     Delete backup
// @Description Deletes the backup record and its archive from storage. Returns 409 if the backup is still running.
// @Tags        backup graph
// @Produce     json
// @Param       id path string true "Backup job ID"
// @Success     204
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /api/v1/backups/{id} [delete]
func (h *BackupHandler) Delete(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
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
	if err := h.cleanExportDir(); err != nil {
		return fmt.Errorf("clean export dir: %w", err)
	}

	log.Info("triggering DGraph export on blue")
	if err := h.triggerBlueExport(ctx); err != nil {
		return fmt.Errorf("trigger blue export: %w", err)
	}

	log.Info("locating exported file")
	dataGZPath, err := h.findExport()
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

	last, err := h.db.Backup.Query().
		Where(backup.StatusIn(backup.StatusCompleted, backup.StatusSkipped)).
		Order(backup.ByCreatedAt(sql.OrderDesc())).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query last backup: %w", err)
	}
	if last != nil && last.Checksum == checksum {
		log.Info("graph unchanged since last backup — skipping upload")
		_, err = h.db.Backup.UpdateOneID(jobID).
			SetStatus(backup.StatusSkipped).
			SetChecksum(checksum).
			SetCompletedAt(time.Now()).
			Save(ctx)
		return err
	}

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
	h.cleanExportDir() //nolint:errcheck

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

	if h.retentionCount > 0 {
		if err := h.enforceRetention(ctx, log); err != nil {
			log.Warn("retention enforcement failed", "err", err)
		}
	}
	return nil
}

func (h *BackupHandler) triggerBlueExport(ctx context.Context) error {
	const mutation = `{"query": "mutation { export(input: { format: \"json\" }) { response { code message } } }"}`
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

func (h *BackupHandler) cleanExportDir() error {
	entries, err := os.ReadDir(h.dgraphExportDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		os.RemoveAll(filepath.Join(h.dgraphExportDir, e.Name())) //nolint:errcheck
	}
	return nil
}

func (h *BackupHandler) findExport() (string, error) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var found string
		filepath.Walk(h.dgraphExportDir, func(path string, info os.FileInfo, err error) error { //nolint:errcheck
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
	return "", fmt.Errorf("no json.gz found in %s after export", h.dgraphExportDir)
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
		Order(backup.ByCreatedAt(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query completed backups: %w", err)
	}
	if len(completed) <= h.retentionCount {
		return nil
	}
	for _, old := range completed[h.retentionCount:] {
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
