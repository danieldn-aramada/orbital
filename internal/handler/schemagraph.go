package handler

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/armada/orbital/internal/configitems"
)

// SchemaGraphHandler serves the deployed schema as a graph. Shared by orbital
// and orb: each passes the field source reading its own DGraph.
type SchemaGraphHandler struct {
	fields *SharedFields
	logger *slog.Logger
}

func NewSchemaGraph(fields *SharedFields, logger *slog.Logger) *SchemaGraphHandler {
	return &SchemaGraphHandler{fields: fields, logger: logger}
}

// Get returns the deployed schema as nodes and edges.
//
// @Summary     Schema graph
// @Description The GraphQL schema DGraph is running, as a graph: one node per ConfigItem type or interface, one edge per relationship. A relationship declared with `@hasInverse` is ONE edge carrying both field names (`field` on the source, `inverse` on the target) and its cardinality.
// @Description
// @Description **Drawing it top-down needs no rules of your own.** Each node carries its `level` (0 at the top). A type sits one level below the type it lives inside — one it cannot exist without, or one its orbId is built from — and the single edge that places it there has `setsLevel: true`. Every other edge is a cross-link: lay out with the `setsLevel` edges only, then draw the rest on top, or a layered layout will move types between levels. Levels are a drawing rule, not an ownership claim.
// @Description
// @Description `ConfigItem` is a node with no edges: every type implements it. Positions are not included; they belong to whichever library draws the graph.
// @Tags        schema
// @Produce     json
// @Success     200 {object} configitems.SchemaGraph
// @Failure     503 {object} errorResponse "The deployed schema could not be read"
// @Router      /api/v1/schema/graph [get]
func (h *SchemaGraphHandler) Get(c echo.Context) error {
	var g configitems.SchemaGraph
	g, err := h.fields.SchemaGraph(c.Request().Context())
	if err != nil {
		h.logger.Warn("schema graph: deployed schema unreadable", "err", err)
		return writeError(c, http.StatusServiceUnavailable, CodeUnavailable,
			"the deployed schema could not be read",
			"check that DGraph is reachable and has a GraphQL schema applied")
	}
	return c.JSON(http.StatusOK, g)
}
