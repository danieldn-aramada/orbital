package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/labstack/echo/v4"
)

// Export proxies the DGraph export mutation to the DGraph admin endpoint.
// Note: this triggers a full-graph export — scoped per-datacenter export is not yet implemented.
// The exported files are written to the DGraph container's /dgraph/export directory.
type Export struct {
	dgraphAdminURL string
}

func NewExport(dgraphAdminURL string) *Export {
	return &Export{dgraphAdminURL: dgraphAdminURL}
}

const exportMutation = `{ "query": "mutation { export(input: { format: \"json\" }) { response { code message } } }" }`

func (h *Export) Trigger(c echo.Context) error {
	resp, err := http.Post(h.dgraphAdminURL, "application/json", bytes.NewBufferString(exportMutation))
	if err != nil {
		return fmt.Errorf("dgraph export: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read export response: %w", err)
	}

	c.Response().Header().Set("Content-Type", "application/json")
	c.Response().WriteHeader(resp.StatusCode)
	_, err = c.Response().Write(body)
	return err
}
