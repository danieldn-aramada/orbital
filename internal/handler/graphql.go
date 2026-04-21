package handler

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
)

type GraphQL struct {
	dgraphURL string
}

func NewGraphQL(dgraphURL string) *GraphQL {
	return &GraphQL{dgraphURL: dgraphURL}
}

// Handle proxies GraphQL requests to DGraph and serves GraphiQL on GET.
//
// @Summary     GraphQL endpoint
// @Description POST: proxies GraphQL queries and mutations to DGraph. GET: serves the GraphiQL explorer UI.
// @Tags        graphql
// @Accept      json
// @Produce     json
// @Param       body body string true "GraphQL request body" example("{\"query\": \"{ queryDataCenter { id name } }\"}")
// @Success     200 {object} map[string]interface{}
// @Router      /graphql [post]
func (h *GraphQL) Handle(c echo.Context) error {
	if c.Request().Method == http.MethodPost {
		resp, err := http.Post(h.dgraphURL, "application/json", c.Request().Body)
		if err != nil {
			return fmt.Errorf("proxy to dgraph: %w", err)
		}
		defer resp.Body.Close()

		c.Response().Header().Set("Content-Type", "application/json")
		_, err = io.Copy(c.Response().Writer, resp.Body)
		return err
	}

	slog.Info("GET /graphql")
	return c.File("internal/static/index.html")
}
