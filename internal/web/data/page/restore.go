package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

type BackupOption struct {
	ID            string
	Label         string
	SchemaVersion string // empty for legacy backups
}

type Restore struct {
	layout.Base
	PageTitle            string
	BackupEnabled        bool
	CompletedBackups     []BackupOption
	CurrentSchemaVersion string
}
