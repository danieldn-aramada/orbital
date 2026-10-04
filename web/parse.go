package web

import (
	"io/fs"
	"os"
)

// Dir is the filesystem templates and static assets are read from: the
// embedded tree in production, the working tree when hot-reloading.
//
// Reading the env var here rather than taking a config value is deliberate and
// is the only place orbital does it. Nine call sites across BOTH apps parse a
// fragment template on the fly — audit rows, job tbodies, the layers modal, the
// delete preview — and none of them carries an fs.FS or a Config. Threading one
// through seven constructors to reach a `template.ParseFS` would be a lot of
// signature churn for a value that is constant for the process lifetime.
//
// The failure this prevents: every one of those sites used
// `template.ParseFiles("web/templates/...")`, which resolves against the
// PROCESS WORKING DIRECTORY. The binary therefore only ran from the repo root —
// anywhere else it panicked at startup or 500'd on first use, which is exactly
// what a container image does not guarantee.
func Dir() fs.FS {
	if os.Getenv("ORBITAL_TEMPLATE_HOT_RELOAD_ENABLED") == "true" ||
		os.Getenv("ORB_TEMPLATE_HOT_RELOAD_ENABLED") == "true" {
		if _, err := os.Stat("web/templates"); err == nil {
			return os.DirFS("web")
		}
	}
	return FS
}
