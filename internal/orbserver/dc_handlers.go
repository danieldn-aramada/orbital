package orbserver

import (
	"github.com/armada/orbital/internal/orb"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

// --- Page data types ---

type dcPageData struct {
	layout.Base
	PageTitle string
}

type serversPageData struct {
	layout.Base
	PageTitle string
}

// --- Page handlers ---

func (s *Server) dcPage(c echo.Context) error {
	b := s.orbBase(c)
	return s.render(c, "datacenter", dcPageData{Base: b, PageTitle: "Data Center"})
}

func (s *Server) serversPage(c echo.Context) error {
	b := s.orbBase(c)
	return s.render(c, "servers", serversPageData{Base: b, PageTitle: "Servers"})
}

// --- Import history page ---

type importHistoryPageData struct {
	layout.Base
	PageTitle string
	History   []orb.ImportRecord
}

func (s *Server) importHistoryPage(c echo.Context) error {
	history, err := orb.LoadHistory(s.cfg.DataDir)
	if err != nil {
		s.logger.Warn("failed to load import history", "err", err)
		history = nil
	}
	// Reverse to show newest first.
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	b := s.orbBase(c)
	return s.render(c, "import-history", importHistoryPageData{
		Base:      b,
		PageTitle: "Import History",
		History:   history,
	})
}
