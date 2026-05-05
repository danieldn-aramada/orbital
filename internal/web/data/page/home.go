package page

import (
	"github.com/armada/orbital/internal/web/data/component"
	"github.com/armada/orbital/internal/web/data/layout"
)

type Home struct {
	layout.Base
	component.Menu

	AppVersion string `json:"appVersion"`
	PageTitle  string `json:"pageTile"`
	Url        string
}
