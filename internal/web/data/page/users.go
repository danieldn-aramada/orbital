package page

import (
	"github.com/armada/orbital/internal/web/data/layout"
)

type UserRow struct {
	ID        int
	Email     string
	Name      string
	Role      string
	CreatedAt string // pre-formatted: "2006-01-02"
}

type Users struct {
	layout.Base
	PageTitle string
	Users     []UserRow
}
