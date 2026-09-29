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

	// Orphans are types the graph still holds nodes for that the deployed
	// schema no longer declares. Their rows cannot be rendered — every field
	// on them resolves to nothing — so the page hides them and says which
	// types they were. The CLIENT cannot name them: that is exactly the
	// information the missing type took with it.
	Orphans []OrphanRow

	// OrphansMore is how many further orphaned types exist beyond the ones in
	// Orphans. Capped server-side: a schema that dropped nine types should not
	// print nine lines above the table when the Schema page carries them all.
	OrphansMore int
}

type Servers struct {
	layout.Base
	PageTitle string
}

type Clusters struct {
	layout.Base
	PageTitle string
}

type NetworkDevices struct {
	layout.Base
	PageTitle string
}
