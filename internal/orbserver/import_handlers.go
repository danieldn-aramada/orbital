package orbserver

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/armada/orbital/internal/oci"
	"github.com/armada/orbital/internal/orb"
	"github.com/labstack/echo/v4"
)

// @Summary     Trigger import
// @Description Starts an async OCI artifact pull and DGraph import for the requested tag. Returns 409 if an import is already running.
// @Tags        import
// @Accept      json
// @Produce     json
// @Param       body body object true "Import request" SchemaExample({"tag":"v3"})
// @Success     202 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /import [post]
func (s *Server) triggerImport(c echo.Context) error {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := c.Bind(&req); err != nil || req.Tag == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "tag is required"})
	}

	snap := s.state.snapshot()
	if snap.Status == "running" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "import already running"})
	}

	s.state.setRunning()

	go func() {
		ctx := context.Background()

		pullCfg := oci.PullConfig{
			Registry:  s.cfg.OCIRegistry,
			Repo:      s.cfg.OCIRepo,
			Username:  s.cfg.OCIUsername,
			Password:  s.cfg.OCIPassword,
			AllowHTTP: s.cfg.OCIAllowHTTP,
		}

		artifact, err := oci.Pull(ctx, pullCfg, req.Tag)
		if err != nil {
			s.state.setFailed("pull: " + err.Error())
			return
		}

		verifyCfg := oci.VerifyConfig{
			PublicKeyPath: s.cfg.OCIPublicKeyPath,
			AllowHTTP:     s.cfg.OCIAllowHTTP,
		}
		repoRef := s.cfg.OCIRegistry + "/" + s.cfg.OCIRepo
		result, err := oci.Verify(ctx, verifyCfg, repoRef, artifact.Digest, s.logger)
		if err != nil {
			s.state.setFailed("verify: " + err.Error())
			return
		}
		s.logger.Info("cosign verification", "result", result.Message)

		meta := orb.ImportMeta{
			Tag:         artifact.Tag,
			Digest:      artifact.Digest,
			DCOrbID:     artifact.Annotations["com.armada.orbital.datacenter-id"],
			ExportJobID: artifact.Annotations["com.armada.orbital.export-job-id"],
			CreatedAt:   time.Now().UTC(),
			Verified:    result.Verified,
		}

		if err := s.imp.Import(ctx, artifact.DataGZ, artifact.SchemaGZ, meta); err != nil {
			s.state.setFailed("import: " + err.Error())
			return
		}

		s.state.setDone(orb.ImportRecord{
			Tag:         meta.Tag,
			Digest:      meta.Digest,
			DCOrbID:     meta.DCOrbID,
			ExportJobID: meta.ExportJobID,
			ImportedAt:  time.Now().UTC(),
			Status:      "done",
			Verified:    result.Verified,
		})
	}()

	return c.JSON(http.StatusAccepted, map[string]string{"status": "started", "tag": req.Tag})
}

// @Summary     Import status
// @Description Returns the current import state snapshot including status, current version, and last import record.
// @Tags        import
// @Produce     json
// @Success     200 {object} importSnapshot
// @Router      /import/status [get]
func (s *Server) importStatus(c echo.Context) error {
	return c.JSON(http.StatusOK, s.state.snapshot())
}

type tagInfo struct {
	Name      string `json:"name"`
	Verified  bool   `json:"verified"`
	SizeBytes int64  `json:"sizeBytes"`
	Digest    string `json:"digest"`
}

// @Summary     List import tags
// @Description Lists available OCI artifact tags from the configured registry for this data center, enriched with signature verification status and artifact size.
// @Tags        import
// @Produce     json
// @Success     200 {object} map[string][]tagInfo
// @Router      /import/tags [get]
func (s *Server) importTags(c echo.Context) error {
	ctx := c.Request().Context()
	pullCfg := oci.PullConfig{
		Registry:  s.cfg.OCIRegistry,
		Repo:      s.cfg.OCIRepo,
		Username:  s.cfg.OCIUsername,
		Password:  s.cfg.OCIPassword,
		AllowHTTP: s.cfg.OCIAllowHTTP,
	}
	allTags, err := oci.ListTags(ctx, pullCfg)
	if err != nil {
		s.logger.Warn("list tags failed", "err", err)
		return c.JSON(http.StatusOK, map[string][]tagInfo{"tags": {}})
	}

	verifyCfg := oci.VerifyConfig{
		PublicKeyPath: s.cfg.OCIPublicKeyPath,
		AllowHTTP:     s.cfg.OCIAllowHTTP,
	}
	repoRef := s.cfg.OCIRegistry + "/" + s.cfg.OCIRepo

	var infos []tagInfo
	for _, t := range allTags {
		// Skip cosign signature tags — not importable artifacts.
		if strings.HasSuffix(t, ".sig") {
			continue
		}
		info := tagInfo{Name: t}
		meta, err := oci.ResolveTag(ctx, pullCfg, t)
		if err != nil {
			s.logger.Warn("resolve tag failed", "tag", t, "err", err)
			infos = append(infos, info)
			continue
		}
		info.SizeBytes = meta.TotalSize
		info.Digest = meta.Digest
		result, err := oci.Verify(ctx, verifyCfg, repoRef, meta.Digest, s.logger)
		if err == nil {
			info.Verified = result.Verified
		}
		infos = append(infos, info)
	}
	return c.JSON(http.StatusOK, map[string][]tagInfo{"tags": infos})
}

// @Summary     Import history
// @Description Returns the rolling history of completed and failed imports from disk.
// @Tags        import
// @Produce     json
// @Success     200 {array}  orb.ImportRecord
// @Router      /import/history [get]
func (s *Server) importHistory(c echo.Context) error {
	records, err := orb.LoadHistory(s.cfg.DataDir)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if records == nil {
		records = []orb.ImportRecord{}
	}
	return c.JSON(http.StatusOK, records)
}

// @Summary     Upload subgraph bundle (courier)
// @Description Accepts a zip bundle (data.json.gz + schema.gz) exported from orbital and imports it directly — no registry required. Use this when delivering a subgraph via physical media or manual transfer.
// @Tags        import
// @Accept      multipart/form-data
// @Produce     json
// @Param       bundle formData file true "Zip archive containing data.json.gz and schema.gz"
// @Success     202 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /import/upload [post]
// uploadImport reads the entire zip into memory before extracting. At peak it holds
// the raw zip + the extracted data.json.gz simultaneously (~2× zip size). This is
// acceptable for typical single-DC subgraphs (1–10 MB compressed). If subgraph size
// grows significantly, rework to: save upload to a temp file, use zip.OpenReader, and
// stream data.json.gz directly to the scratch path — eliminating the double-copy.
func (s *Server) uploadImport(c echo.Context) error {
	if snap := s.state.snapshot(); snap.Status == "running" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "import already running"})
	}

	fh, err := c.FormFile("bundle")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "bundle file is required"})
	}

	f, err := fh.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "could not open upload"})
	}
	defer f.Close()

	raw, err := io.ReadAll(f)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "could not read upload"})
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "not a valid zip archive"})
	}

	var dataGZ, schemaGZ []byte
	for _, zf := range zr.File {
		switch zf.Name {
		case "data.json.gz":
			rc, err := zf.Open()
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read data.json.gz"})
			}
			dataGZ, _ = io.ReadAll(rc)
			rc.Close()
		case "schema.gz":
			rc, err := zf.Open()
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read schema.gz"})
			}
			schemaGZ, _ = io.ReadAll(rc)
			rc.Close()
		}
	}

	if len(dataGZ) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "zip must contain data.json.gz"})
	}
	if len(schemaGZ) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "zip must contain schema.gz"})
	}

	tag := fmt.Sprintf("courier-%s", time.Now().UTC().Format("20060102-150405"))
	s.state.setRunning()

	go func() {
		meta := orb.ImportMeta{
			Tag:       tag,
			CreatedAt: time.Now().UTC(),
		}
		if err := s.imp.Import(context.Background(), dataGZ, schemaGZ, meta); err != nil {
			s.state.setFailed("courier import: " + err.Error())
			return
		}
		s.state.setDone(orb.ImportRecord{
			Tag:        meta.Tag,
			ImportedAt: time.Now().UTC(),
			Status:     "done",
		})
	}()

	return c.JSON(http.StatusAccepted, map[string]string{"status": "started", "tag": tag})
}
