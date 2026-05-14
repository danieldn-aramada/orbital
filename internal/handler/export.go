package handler

import (
	"archive/zip"
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

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/ent/restorejob"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type Export struct {
	db                    *ent.Client
	dgraphURL             string // blue GraphQL
	dgraphScratchURL      string // scratch GraphQL
	dgraphScratchAdminURL string // scratch admin
	exportDir             string // where final zips are written
	scratchExportDir      string // host-side mount of /dgraph/export in scratch container
	schemaPath            string // path to the GraphQL schema file
	logger                *slog.Logger
}

func NewExport(db *ent.Client, dgraphURL, dgraphScratchURL, dgraphScratchAdminURL, exportDir, scratchExportDir, schemaPath string, logger *slog.Logger) *Export {
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		logger.Warn("could not create export dir", "dir", exportDir, "err", err)
	}
	if err := os.MkdirAll(scratchExportDir, 0o755); err != nil {
		logger.Warn("could not create scratch export dir", "dir", scratchExportDir, "err", err)
	}
	return &Export{
		db:                    db,
		dgraphURL:             dgraphURL,
		dgraphScratchURL:      dgraphScratchURL,
		dgraphScratchAdminURL: dgraphScratchAdminURL,
		exportDir:             exportDir,
		scratchExportDir:      scratchExportDir,
		schemaPath:            schemaPath,
		logger:                logger,
	}
}

type triggerResponse struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
}

type statusResponse struct {
	JobID        string  `json:"jobId"`
	DataCenter   string  `json:"dataCenter"`
	Status       string  `json:"status"`
	Published    bool    `json:"published"`
	Error        *string `json:"error,omitempty"`
	StartedAt    *string `json:"startedAt,omitempty"`
	CompletedAt  *string `json:"completedAt,omitempty"`
	CreatedAt    string  `json:"createdAt"`
}

// Trigger handles POST /api/v1/datacenters/:id/export
//
// @Summary     Trigger subgraph export
// @Description Triggers an async export of the data center's configuration subgraph. Returns immediately with a job ID. Returns 409 if an export is already in progress for this data center.
// @Tags        export subgraph
// @Produce     json
// @Param       id path string true "Data center ID"
// @Success     202 {object} triggerResponse
// @Failure     409 {object} map[string]string
// @Router      /api/v1/datacenters/{id}/export [post]
func (h *Export) Trigger(c echo.Context) error {
	datacenterID := c.Param("id")

	dcName, _, err := h.fetchDCInfo(c.Request().Context(), datacenterID)
	if err != nil {
		h.logger.Warn("could not fetch DC info", "id", datacenterID, "err", err)
		dcName = datacenterID
	}

	// Scratch DGraph is shared — only one export can run at a time across all data centers.
	existing, err := h.db.ExportJob.Query().
		Where(exportjob.StatusIn(exportjob.StatusPending, exportjob.StatusRunning)).
		First(c.Request().Context())
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("check existing job: %w", err)
	}
	if existing != nil {
		return c.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("export already in progress (jobId: %s)", existing.ID),
			"jobId": existing.ID.String(),
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

	job, err := h.db.ExportJob.Create().
		SetDatacenterID(datacenterID).
		SetDatacenterName(dcName).
		SetStatus(exportjob.StatusPending).
		Save(c.Request().Context())
	if err != nil {
		return fmt.Errorf("create export job: %w", err)
	}

	go h.runExport(job.ID)

	actor, _ := c.Get("user_email").(string)
	writeAuditEvent(h.db, h.logger, actor, "exportSubgraph",
		[]string{"exportSubgraph"},
		[]string{"DataCenter"},
		[]string{dcName},
		map[string]any{
			"jobId":          job.ID.String(),
			"datacenterId":   datacenterID,
			"datacenterName": dcName,
		},
	)

	return c.JSON(http.StatusAccepted, triggerResponse{
		JobID:  job.ID.String(),
		Status: string(job.Status),
	})
}

// List handles GET /api/v1/export/jobs
//
// @Summary     List export jobs
// @Description Returns the 50 most recent export jobs ordered by creation time.
// @Tags        export subgraph
// @Produce     json
// @Success     200 {array} statusResponse
// @Router      /api/v1/export/jobs [get]
func (h *Export) List(c echo.Context) error {
	jobs, err := h.db.ExportJob.Query().
		Order(exportjob.ByCreatedAt(sql.OrderDesc())).
		Limit(50).
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}

	// Detect stale jobs: completed jobs whose artifact file no longer exists.
	for _, job := range jobs {
		if job.Status == exportjob.StatusCompleted && job.ArtifactPath != nil {
			if _, statErr := os.Stat(*job.ArtifactPath); os.IsNotExist(statErr) {
				h.db.ExportJob.UpdateOneID(job.ID). //nolint:errcheck
					SetStatus(exportjob.StatusStale).
					Save(c.Request().Context())
				job.Status = exportjob.StatusStale
			}
		}
	}

	// Build a set of job IDs that have at least one published artifact.
	publishedJobIDs := map[uuid.UUID]bool{}
	if len(jobs) > 0 {
		jobIDs := make([]uuid.UUID, 0, len(jobs))
		for _, j := range jobs {
			jobIDs = append(jobIDs, j.ID)
		}
		artifactRows, err := h.db.RegistryArtifact.Query().
			Where(registryartifact.ExportJobIDIn(jobIDs...)).
			Select(registryartifact.FieldExportJobID).
			All(c.Request().Context())
		if err == nil {
			for _, a := range artifactRows {
				publishedJobIDs[a.ExportJobID] = true
			}
		}
	}

	out := make([]statusResponse, 0, len(jobs))
	for _, job := range jobs {
		r := statusResponse{
			JobID:      job.ID.String(),
			DataCenter: job.DatacenterName,
			Status:     string(job.Status),
			Published:  publishedJobIDs[job.ID],
			CreatedAt:  job.CreatedAt.Format(time.RFC3339),
		}
		if job.Error != nil {
			r.Error = job.Error
		}
		if job.StartedAt != nil {
			s := job.StartedAt.Format(time.RFC3339)
			r.StartedAt = &s
		}
		if job.CompletedAt != nil {
			s := job.CompletedAt.Format(time.RFC3339)
			r.CompletedAt = &s
		}
		out = append(out, r)
	}
	return c.JSON(http.StatusOK, out)
}

// Status handles GET /api/v1/export/jobs/:jobId
//
// @Summary     Get export job status
// @Description Returns the current status of an export job.
// @Tags        export subgraph
// @Produce     json
// @Param       jobId path string true "Job ID (UUID)"
// @Success     200 {object} statusResponse
// @Failure     404
// @Router      /api/v1/export/jobs/{jobId} [get]
func (h *Export) Status(c echo.Context) error {
	id, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}

	job, err := h.db.ExportJob.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get job: %w", err)
	}

	resp := statusResponse{
		JobID:      job.ID.String(),
		DataCenter: job.DatacenterName,
		Status:     string(job.Status),
		CreatedAt:  job.CreatedAt.Format(time.RFC3339),
	}
	if job.Error != nil {
		resp.Error = job.Error
	}
	if job.StartedAt != nil {
		s := job.StartedAt.Format(time.RFC3339)
		resp.StartedAt = &s
	}
	if job.CompletedAt != nil {
		s := job.CompletedAt.Format(time.RFC3339)
		resp.CompletedAt = &s
	}

	return c.JSON(http.StatusOK, resp)
}

// Download handles GET /api/v1/export/jobs/:jobId/download
//
// @Summary     Download export artifact
// @Description Downloads the export artifact as a zip archive containing data.json.gz and schema.gz.
// @Tags        export subgraph
// @Produce     application/zip
// @Param       jobId path string true "Job ID (UUID)"
// @Success     200
// @Failure     404
// @Router      /api/v1/export/jobs/{jobId}/download [get]
func (h *Export) Download(c echo.Context) error {
	id, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}

	job, err := h.db.ExportJob.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get job: %w", err)
	}

	if job.Status != exportjob.StatusCompleted || job.ArtifactPath == nil {
		return echo.ErrNotFound
	}

	f, err := os.Open(*job.ArtifactPath)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer f.Close()

	filename := fmt.Sprintf("%s-%s.zip", job.DatacenterName, job.ID)
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	return c.Stream(http.StatusOK, "application/zip", f)
}

// runExport is the async goroutine that drives the export workflow.
func (h *Export) runExport(jobID uuid.UUID) {
	ctx := context.Background()
	log := h.logger.With("jobId", jobID)

	_, err := h.db.ExportJob.UpdateOneID(jobID).
		SetStatus(exportjob.StatusRunning).
		SetStartedAt(time.Now()).
		Save(ctx)
	if err != nil {
		log.Error("failed to mark job running", "err", err)
		return
	}

	if err := h.doExport(ctx, jobID, log); err != nil {
		log.Error("export failed", "err", err)
		errStr := err.Error()
		h.db.ExportJob.UpdateOneID(jobID). //nolint:errcheck
						SetStatus(exportjob.StatusFailed).
						SetError(errStr).
						Save(ctx)
	}
}

func (h *Export) doExport(ctx context.Context, jobID uuid.UUID, log *slog.Logger) error {
	job, err := h.db.ExportJob.Get(ctx, jobID)
	if err != nil {
		return fmt.Errorf("get job: %w", err)
	}

	// 1. Resolve namespace name from DC ID
	log.Info("resolving DC namespace")
	_, namespaceName, err := h.fetchDCInfo(ctx, job.DatacenterID)
	if err != nil {
		return fmt.Errorf("fetch DC info: %w", err)
	}

	// 2. Query the full namespace subgraph from blue via DQL.
	// Uses has(ConfigItem.namespace) + uid_in to find every node in the namespace
	// regardless of type, then expand(_all_) to get all predicates without
	// enumerating schema types. New ConfigItem types are automatically included.
	log.Info("querying namespace subgraph from blue DGraph", "namespace", namespaceName)
	nodes, err := h.fetchNamespaceSubgraph(ctx, namespaceName)
	if err != nil {
		return fmt.Errorf("fetch namespace subgraph: %w", err)
	}
	log.Info("subgraph fetched", "nodes", len(nodes))
	if len(nodes) == 0 {
		return fmt.Errorf("namespace %q has no nodes in blue DGraph — nothing to export", namespaceName)
	}

	// 3. Apply schema to scratch so GraphQL layer is aware of all types before
	// loading data. Safe to run after a manual wipe.
	log.Info("applying schema to scratch DGraph")
	if err := h.applyScratchSchema(ctx); err != nil {
		return fmt.Errorf("apply scratch schema: %w", err)
	}

	// 4. Load subgraph into scratch via DQL mutation, preserving original UIDs
	// from blue so the resulting export has consistent, stable UIDs for orb import.
	log.Info("loading subgraph into scratch DGraph")
	if err := h.loadSubgraphIntoScratch(ctx, nodes); err != nil {
		return fmt.Errorf("load subgraph into scratch: %w", err)
	}

	// 6. Create a per-job directory under the scratch export mount.
	// Host path:      scratchExportDir/<jobID>/
	// Container path: /dgraph/export/<jobID>/   (passed as destination in the export mutation)
	// This gives each job an isolated, clearly-labelled output directory.
	jobScratchDir := filepath.Join(h.scratchExportDir, jobID.String())
	if err := os.MkdirAll(jobScratchDir, 0o755); err != nil {
		return fmt.Errorf("create job scratch dir: %w", err)
	}

	jobContainerDir := "/dgraph/export/" + jobID.String()

	// 7. Trigger native DGraph export mutation on scratch, scoped to the job's directory.
	log.Info("triggering native DGraph export on scratch", "destination", jobContainerDir)
	if err := h.triggerScratchExport(ctx, jobContainerDir); err != nil {
		return fmt.Errorf("trigger scratch export: %w", err)
	}

	// 8. Find the exported json.gz written by DGraph into the job's directory.
	log.Info("locating exported data file", "dir", jobScratchDir)
	dataGZPath, err := h.findScratchExport(jobScratchDir)
	if err != nil {
		return fmt.Errorf("find scratch export: %w", err)
	}
	log.Info("found exported file", "path", dataGZPath)

	// 9. Read data.json.gz (already gzipped by DGraph — do not re-gzip)
	dataGZ, err := os.ReadFile(dataGZPath)
	if err != nil {
		return fmt.Errorf("read data.json.gz: %w", err)
	}

	// 10. Read and gzip the schema
	schemaBytes, err := os.ReadFile(h.schemaPath)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	schemaGZ, err := gzipBytes(schemaBytes)
	if err != nil {
		return fmt.Errorf("gzip schema: %w", err)
	}

	// 11. Write zip archive
	zipPath := filepath.Join(h.exportDir, fmt.Sprintf("orbital-export-%s.zip", jobID))
	if err := writeZip(zipPath, dataGZ, schemaGZ, nil); err != nil {
		return fmt.Errorf("write zip: %w", err)
	}
	log.Info("artifact written", "path", zipPath)

	// 12. Mark completed
	_, err = h.db.ExportJob.UpdateOneID(jobID).
		SetStatus(exportjob.StatusCompleted).
		SetArtifactPath(zipPath).
		SetCompletedAt(time.Now()).
		Save(ctx)
	return err
}

// ── DGraph helpers ────────────────────────────────────────────────────────────

// dqlBase derives the DQL HTTP root from a GraphQL URL.
// e.g. http://localhost:8080/graphql → http://localhost:8080
func dqlBase(graphqlURL string) string {
	return strings.TrimSuffix(graphqlURL, "/graphql")
}

// fetchDCInfo queries blue GraphQL for the DC name and its namespace name.
func (h *Export) fetchDCInfo(ctx context.Context, datacenterID string) (name, namespaceName string, err error) {
	query := fmt.Sprintf(`{ getDataCenter(id: %q) { name namespace { name } } }`, datacenterID)
	body, _ := json.Marshal(map[string]string{"query": query})
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			GetDataCenter struct {
				Name      string `json:"name"`
				Namespace struct {
					Name string `json:"name"`
				} `json:"namespace"`
			} `json:"getDataCenter"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", err
	}
	dc := result.Data.GetDataCenter
	return dc.Name, dc.Namespace.Name, nil
}

// fetchUIDPredicates queries the DGraph schema and returns all predicate names
// whose type is uid. These must be listed explicitly in DQL queries — expand(_all_)
// only returns scalar predicates.
func (h *Export) fetchUIDPredicates(ctx context.Context) ([]string, error) {
	payload := map[string]string{"query": "schema {}"}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(dqlBase(h.dgraphURL)+"/query", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("schema query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("schema query failed (%d): %s", resp.StatusCode, b)
	}
	var result struct {
		Data struct {
			Schema []struct {
				Predicate string `json:"predicate"`
				Type      string `json:"type"`
			} `json:"schema"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode schema response: %w", err)
	}
	var preds []string
	for _, s := range result.Data.Schema {
		if s.Type == "uid" {
			preds = append(preds, s.Predicate)
		}
	}
	return preds, nil
}

// fetchNamespaceSubgraph retrieves every node in the target namespace from blue
// using two DQL result blocks that are merged in Go:
//
//  1. "items" — expand(_all_) for scalar predicates. DGraph silently drops UID
//     predicates from expand(_all_) when they form cycles (which all edges in our
//     schema do), so scalars and edges must be fetched separately.
//
//  2. "edges" — explicit listing of every UID-type predicate with { uid } sub-
//     selection. The predicate list is derived from the live DGraph schema so new
//     edge types are included automatically without code changes.
//
// The two result sets are merged by UID before being sent to scratch as a single
// DQL mutation, ensuring UIDs, scalar predicates, and edges are all written in one
// pass.
func (h *Export) fetchNamespaceSubgraph(ctx context.Context, namespaceName string) ([]map[string]any, error) {
	uidPreds, err := h.fetchUIDPredicates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch uid predicates: %w", err)
	}

	var edgeLines strings.Builder
	for _, p := range uidPreds {
		fmt.Fprintf(&edgeLines, "\t\t\t%s { uid }\n", p)
	}

	dql := fmt.Sprintf(`{
		var(func: type(Namespace)) @filter(eq(Namespace.name, %q)) { NS as uid }
		ns(func: uid(NS)) { uid dgraph.type expand(_all_) }
		items(func: has(ConfigItem.namespace)) @filter(uid_in(ConfigItem.namespace, uid(NS))) {
			uid
			dgraph.type
			expand(_all_)
		}
		edges(func: has(ConfigItem.namespace)) @filter(uid_in(ConfigItem.namespace, uid(NS))) {
			uid
			%s
		}
	}`, namespaceName, edgeLines.String())

	payload := map[string]string{"query": dql}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(dqlBase(h.dgraphURL)+"/query", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dql query: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dql query failed (%d): %s", resp.StatusCode, b)
	}

	var result struct {
		Data struct {
			Ns    []map[string]any `json:"ns"`
			Items []map[string]any `json:"items"`
			Edges []map[string]any `json:"edges"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode dql response: %w", err)
	}

	if len(result.Data.Ns) == 0 {
		return nil, fmt.Errorf("namespace %q not found in DGraph", namespaceName)
	}

	// Merge edge predicates into the corresponding scalar nodes by UID.
	edgesByUID := make(map[string]map[string]any, len(result.Data.Edges))
	for _, e := range result.Data.Edges {
		if uid, ok := e["uid"].(string); ok {
			edgesByUID[uid] = e
		}
	}
	for _, node := range result.Data.Items {
		uid, ok := node["uid"].(string)
		if !ok {
			continue
		}
		if edges, ok := edgesByUID[uid]; ok {
			for k, v := range edges {
				if k == "uid" {
					continue
				}
				node[k] = v
			}
		}
	}

	nodes := make([]map[string]any, 0, 1+len(result.Data.Items))
	nodes = append(nodes, result.Data.Ns...)
	nodes = append(nodes, result.Data.Items...)
	return nodes, nil
}

// loadSubgraphIntoScratch inserts all nodes into scratch via DQL mutate.
// Original UIDs from blue are preserved in the mutation so that relationships
// remain intact and the resulting DGraph export has stable UIDs for orb import.
func (h *Export) loadSubgraphIntoScratch(ctx context.Context, nodes []map[string]any) error {
	payload := map[string]any{"set": nodes}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	mutateURL := dqlBase(h.dgraphScratchURL) + "/mutate?commitNow=true"
	h.logger.Info("posting subgraph to scratch", "url", mutateURL, "nodes", len(nodes))
	resp, err := http.Post(mutateURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dql mutate: %w", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	h.logger.Info("scratch mutate response", "status", resp.StatusCode, "body", string(b))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("dql mutate failed (%d): %s", resp.StatusCode, b)
	}
	var mutResp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &mutResp); err == nil && len(mutResp.Errors) > 0 {
		return fmt.Errorf("dql mutate error: %s", mutResp.Errors[0].Message)
	}
	return nil
}

func (h *Export) wipeScratch(ctx context.Context) error {
	alterURL := dqlBase(h.dgraphScratchURL) + "/alter"
	resp, err := http.Post(alterURL, "application/json", strings.NewReader(`{"drop_all": true}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("alter failed (%d): %s", resp.StatusCode, b)
	}
	return nil
}

func (h *Export) applyScratchSchema(ctx context.Context) error {
	schemaBytes, err := os.ReadFile(h.schemaPath)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	schemaURL := strings.TrimSuffix(h.dgraphScratchAdminURL, "/") + "/schema"
	resp, err := http.Post(schemaURL, "application/octet-stream", bytes.NewReader(schemaBytes))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("schema apply failed (%d): %s", resp.StatusCode, b)
	}
	return nil
}

func (h *Export) triggerScratchExport(ctx context.Context, destination string) error {
	mutation := fmt.Sprintf(`{"query": "mutation { export(input: { format: \"json\", destination: \"%s\" }) { response { code message } } }"}`, destination)
	resp, err := http.Post(h.dgraphScratchAdminURL, "application/json", strings.NewReader(mutation))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("export mutation failed (%d): %s", resp.StatusCode, b)
	}
	h.logger.Info("scratch export mutation response", "body", string(b))
	return nil
}

// findScratchExport walks dir and returns the first *.json.gz file.
// DGraph writes to a timestamped subdirectory under the destination path.
// Retries for up to 15 seconds since DGraph may flush the file slightly after the mutation returns.
func (h *Export) findScratchExport(dir string) (string, error) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var found string
		var seen []string
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				seen = append(seen, path)
				if strings.HasSuffix(path, ".json.gz") {
					found = path
					return filepath.SkipAll
				}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		h.logger.Info("scratch export dir contents", "dir", dir, "files", seen)
		if found != "" {
			return found, nil
		}
		time.Sleep(1 * time.Second)
	}
	return "", fmt.Errorf("no json.gz found in %s after export", dir)
}

// ── Archive helpers ───────────────────────────────────────────────────────────

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeZip(path string, dataGZ, dqlSchemaGZ, gqlSchemaGZ []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"data.json.gz", dataGZ},
		{"schema.gz", dqlSchemaGZ},
		{"gql_schema.gz", gqlSchemaGZ},
	} {
		if entry.data == nil {
			continue
		}
		w, err := zw.Create(entry.name)
		if err != nil {
			return err
		}
		if _, err := w.Write(entry.data); err != nil {
			return err
		}
	}
	return nil
}
