package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
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
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const dgraphLiveLabelSelector = "app.kubernetes.io/name=dgraph-live"

// RestoreHandler handles async DGraph restore operations.
type RestoreHandler struct {
	db               *ent.Client
	storage          blobStorage
	k8sClient        kubernetes.Interface
	restCfg          *rest.Config
	dgraphAlterURL   string // e.g. http://dgraph-blue:8080/alter
	dgraphSchemaURL  string // e.g. http://dgraph-blue:8080/admin/schema
	dgraphAlphaGRPC  string // e.g. dgraph-blue-dgraph-alpha:9080
	dgraphZeroGRPC   string // e.g. dgraph-blue-dgraph-zero:5080
	dgraphNamespace  string
	schemaPath       string // path to the GraphQL SDL schema file
	restoreDir       string // PVC mount path shared with dgraph-live pod, e.g. /restore
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
	DGraphNamespace string
	SchemaPath      string // path to the GraphQL SDL schema file
	RestoreDir      string // PVC mount path shared with dgraph-live pod
	RestoreTimeout  time.Duration
}

func NewRestoreHandler(ctx context.Context, db *ent.Client, cfg RestoreConfig, k8sClient kubernetes.Interface, restCfg *rest.Config, logger *slog.Logger) (*RestoreHandler, error) {
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

	base := strings.TrimSuffix(cfg.DGraphAdminURL, "/admin")
	alterURL := base + "/alter"
	schemaURL := base + "/admin/schema"

	return &RestoreHandler{
		db:              db,
		storage:         store,
		k8sClient:       k8sClient,
		restCfg:         restCfg,
		dgraphAlterURL:  alterURL,
		dgraphSchemaURL: schemaURL,
		dgraphAlphaGRPC: cfg.DGraphAlphaGRPC,
		dgraphZeroGRPC:  cfg.DGraphZeroGRPC,
		dgraphNamespace: cfg.DGraphNamespace,
		schemaPath:      cfg.SchemaPath,
		restoreDir:      cfg.RestoreDir,
		restoreTimeout:  cfg.RestoreTimeout,
		logger:          logger,
	}, nil
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
		BackupID string `json:"backupId"`
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
			"error": fmt.Sprintf("export in progress (jobId: %s)", existingExport.ID),
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

	actor, _ := c.Get("user_name").(string)
	if actor == "" {
		actor, _ = c.Get("user_email").(string)
	}
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

// List handles GET /api/v1/restore
//
// @Summary     List restore jobs
// @Description Returns up to 50 restore jobs ordered by most recent first.
// @Tags        backup graph
// @Produce     json
// @Success     200 {array} restoreJobResponse
// @Router      /api/v1/restore [get]
func (h *RestoreHandler) List(c echo.Context) error {
	jobs, err := h.db.RestoreJob.Query().
		Order(restorejob.ByCreatedAt(sql.OrderDesc())).
		Limit(50).
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("list restore jobs: %w", err)
	}
	out := make([]restoreJobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toRestoreJobResponse(j))
	}
	return c.JSON(http.StatusOK, out)
}

// Status handles GET /api/v1/restore/:id
//
// @Summary     Get restore job status
// @Description Returns the status of a specific restore job.
// @Tags        backup graph
// @Produce     json
// @Param       id path string true "Restore job ID"
// @Success     200 {object} restoreJobResponse
// @Failure     404 {object} map[string]string
// @Router      /api/v1/restore/{id} [get]
func (h *RestoreHandler) Status(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
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
		h.db.RestoreJob.UpdateOneID(jobID). //nolint:errcheck
			SetStatus(restorejob.StatusFailed).
			SetError(errStr).
			SetLog(logBuf.String()).
			SetCompletedAt(time.Now()).
			Save(context.Background())
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

	// Fail fast: find the dgraph-live pod before touching anything
	podName, err := h.findDgraphLivePod(ctx)
	if err != nil {
		fail("find dgraph-live pod", err)
		return
	}
	log(fmt.Sprintf("Found dgraph-live pod: %s", podName))

	// Clean up any leftover files from a previous failed restore
	zipPath := filepath.Join(h.restoreDir, "backup.zip")
	dataGzPath := filepath.Join(h.restoreDir, "data.json.gz")
	dqlSchemaGzPath := filepath.Join(h.restoreDir, "schema.gz")
	gqlSchemaGzPath := filepath.Join(h.restoreDir, "gql_schema.gz")
	os.Remove(zipPath)
	os.Remove(dataGzPath)
	os.Remove(dqlSchemaGzPath)
	os.Remove(gqlSchemaGzPath)

	defer func() {
		os.Remove(zipPath)
		os.Remove(dataGzPath)
		os.Remove(dqlSchemaGzPath)
		os.Remove(gqlSchemaGzPath)
	}()

	// Download backup zip to the shared PVC
	log("Downloading backup from storage...")
	if err := h.downloadToFile(ctx, bk.S3Key, zipPath); err != nil {
		fail("download backup", err)
		return
	}

	// Extract all three files from zip onto the shared PVC
	log("Extracting backup...")
	if err := extractBackupZip(zipPath, h.restoreDir); err != nil {
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
	cmd := fmt.Sprintf(
		"dgraph live --files %s --schema %s --alpha %s --zero %s",
		filepath.Join(h.restoreDir, "data.json.gz"),
		filepath.Join(h.restoreDir, "schema.gz"),
		h.dgraphAlphaGRPC,
		h.dgraphZeroGRPC,
	)
	out, err := h.execInPod(ctx, podName, cmd)
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
	h.db.RestoreJob.UpdateOneID(jobID). //nolint:errcheck
		SetStatus(restorejob.StatusCompleted).
		SetLog(logBuf.String()).
		SetCompletedAt(time.Now()).
		Save(context.Background())
}

// downloadToFile downloads the S3 object at s3Key to destPath on disk via a presigned URL.
func (h *RestoreHandler) downloadToFile(ctx context.Context, s3Key, destPath string) error {
	presignedURL, err := h.storage.presignURL(ctx, s3Key)
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

func (h *RestoreHandler) findDgraphLivePod(ctx context.Context) (string, error) {
	pods, err := h.k8sClient.CoreV1().Pods(h.dgraphNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: dgraphLiveLabelSelector,
	})
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}
	return "", fmt.Errorf("no running dgraph-live pod found in namespace %q (selector: %s)", h.dgraphNamespace, dgraphLiveLabelSelector)
}

func (h *RestoreHandler) execInPod(ctx context.Context, podName, cmd string) (string, error) {
	req := h.k8sClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(h.dgraphNamespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"/bin/sh", "-c", cmd},
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(h.restCfg, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\nstderr: " + stderr.String()
	}
	return output, err
}
