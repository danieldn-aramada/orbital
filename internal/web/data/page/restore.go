package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

type Restore struct {
	layout.Base
	PageTitle     string
	BackupEnabled bool
	K8sAvailable  bool
}
