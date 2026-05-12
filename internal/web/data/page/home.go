package page

import (
	"github.com/armada/orbital/internal/web/data/component"
	"github.com/armada/orbital/internal/web/data/layout"
)

type Home struct {
	layout.Base
	component.Menu

	PageTitle string `json:"pageTile"`
	Url       string
}

type Servers struct {
	layout.Base
	PageTitle string
}
