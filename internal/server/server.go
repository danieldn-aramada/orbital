package server

import (
	"context"
	"fmt"

	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

type Server struct {
	cfg  *config.Config
	echo *echo.Echo
}

func New(cfg *config.Config) *Server {
	e := echo.New()
	e.HideBanner = true
	e.Static("/", "internal/static")

	gql := handler.NewGraphQL(cfg.DGraphURL)
	e.Any("/graphql", gql.Handle)

	return &Server{
		cfg:  cfg,
		echo: e,
	}
}

func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		if err := s.echo.Start(":" + s.cfg.Port); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	if err := s.echo.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return nil
}
