package web

import "embed"

//go:embed templates/orb templates/orbital templates/shared shared/static
var FS embed.FS
