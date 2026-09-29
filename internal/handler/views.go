package handler

import (
	"log/slog"
	"net/http"

	"github.com/armada/orbital/internal/configitems"
	"github.com/labstack/echo/v4"
)

// ViewsHandler serves the view list — orbital's equivalent of Kubernetes'
// APIResourceList.
//
// The UI consumes THIS rather than reaching into the registry, which keeps
// orbital's own UI a first-class consumer of the public API (CLAUDE.md) and
// hands orbctl and AEP the same map for free. A second, private path to the
// same information is how the UI and its integrators drift apart.
type ViewsHandler struct {
	fields *SharedFields
	logger *slog.Logger
}

func NewViewsHandler(fields *SharedFields, logger *slog.Logger) *ViewsHandler {
	return &ViewsHandler{fields: fields, logger: logger}
}

// viewsResponse is the response body for GET /api/v1/views.
type viewsResponse struct {
	// Views is every renderable ConfigItem type, sorted by type name.
	Views []configitems.View `json:"views"`
}

// List returns the resolved views.
//
//	@Summary		List renderable views
//	@Description	Every ConfigItem type the deployed schema declares, with the URL slug its pages live under, the editable field list, and its relationships as tabs. Derived from the running schema: a type added to the schema appears here with no code change and no restart.
//	@Tags			views
//	@Produce		json
//	@Success		200	{object}	viewsResponse
//	@Failure		503	{object}	errorResponse	"the schema could not be read from DGraph"
//	@Router			/api/v1/views [get]
func (h *ViewsHandler) List(c echo.Context) error {
	views, err := h.fields.Views(c.Request().Context())
	if err != nil {
		// 503, not 500: the schema is unreadable RIGHT NOW and the resolver
		// self-heals without a restart, so this is retryable rather than a bug
		// in the request.
		h.logger.Warn("could not resolve views", "err", err)
		return writeError(c, http.StatusServiceUnavailable, CodeUnavailable,
			"orbital cannot read the schema from DGraph, so the view list is unknown",
			"Retry shortly — orbital recovers on its own once DGraph is reachable.")
	}
	return c.JSON(http.StatusOK, viewsResponse{Views: views})
}
