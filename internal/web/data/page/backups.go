package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

type Backups struct {
	layout.Base
	PageTitle      string
	BackupEnabled  bool
	S3Endpoint     string
	S3Bucket       string
}
