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
	return c.File("orb/static/index.html")
}
