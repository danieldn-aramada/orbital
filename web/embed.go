package web

import "embed"

//go:embed templates/orb templates/shared shared/static
var FS embed.FS
