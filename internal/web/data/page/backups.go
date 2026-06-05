package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

type Backups struct {
	layout.Base
	PageTitle       string
	BackupEnabled   bool
	S3Endpoint      string
	S3Bucket        string
	HasSchedule     bool
	ScheduleEnabled bool
	ScheduleSummary string // e.g. "Every 24h at 02:00 UTC"
	NextRunApprox   string
	LastTriggeredAt string
}
