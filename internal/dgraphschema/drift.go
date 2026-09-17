package dgraphschema

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Drift returns the declarations present in the shipped SDL but absent from the
// live one, in file order.
//
// Only that direction is checked. Code newer than schema is the failure mode —
// a query for a field DGraph lacks errors, yields zero rows, and renders as a
// 404 or a silently truncated page (real outage 2026-07-27: v0.0.25 queried
// `retentionDays` against a DGraph still on v3 and every cluster 404'd).
// Predicates present in DGraph but not in the shipped file are harmless: DGraph
// schema application is additive at the RDF layer, so an older field lingers
// unqueried rather than breaking anything.
//
// The comparison is line-based rather than parsed because DGraph stores the SDL
// verbatim — the bytes posted to /admin/schema come back from getGQLSchema
// unchanged, comments included. A parser would add a dependency and a second
// definition of "what this schema says" for no extra signal.
func Drift(shipped, live string) []string {
	present := make(map[string]struct{})
	for _, l := range declarations(live) {
		present[l] = struct{}{}
	}
	var missing []string
	seen := make(map[string]struct{})
	for _, l := range declarations(shipped) {
		if _, ok := present[l]; ok {
			continue
		}
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		missing = append(missing, l)
	}
	return missing
}

// declarations reduces SDL to comparable lines: comments stripped, whitespace
// normalized, blanks and bare punctuation dropped. Punctuation-only lines ("}",
// "{") are excluded because they match trivially and carry no signal — a report
// of "} is missing" tells an operator nothing.
func declarations(sdl string) []string {
	var out []string
	for _, raw := range strings.Split(sdl, "\n") {
		if i := strings.IndexByte(raw, '#'); i >= 0 {
			raw = raw[:i]
		}
		l := strings.Join(strings.Fields(raw), " ")
		if l == "" || l == "{" || l == "}" || l == "}{" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// CheckOnce compares the schema file against what DGraph is actually running and
// logs the difference. It never returns an error to the caller and never blocks
// readiness: orbital serves plenty without a matching schema, DGraph may simply
// be slower to come up, and CLAUDE.md settles that /healthz must not depend on
// downstream services.
//
// It exists because nothing else observes this. Orbital does not apply the
// schema at boot (see docs/reference/DGRAPH.md § Schema rules) — an operator has
// to, and until now forgetting produced no signal at all until users hit 404s.
func CheckOnce(ctx context.Context, adminURL, schemaPath string, logger *slog.Logger) {
	shipped, err := os.ReadFile(schemaPath)
	if err != nil {
		logger.Warn("schema drift check skipped — cannot read schema file", "path", schemaPath, "err", err)
		return
	}
	live, err := Active(ctx, adminURL)
	if err != nil {
		logger.Warn("schema drift check skipped — cannot read the applied schema", "admin_url", adminURL, "err", err)
		return
	}
	if strings.TrimSpace(live) == "" {
		logger.Warn("NO GRAPHQL SCHEMA IS APPLIED to DGraph — apply schema/schema.graphql before use; see deploy/README.md step 8", "admin_url", adminURL)
		return
	}
	missing := Drift(string(shipped), live)
	if len(missing) == 0 {
		logger.Info("DGraph schema matches the shipped schema", "path", schemaPath)
		return
	}
	logger.Warn("DGRAPH SCHEMA IS BEHIND THIS BUILD — queries for the declarations below will fail, "+
		"which renders as 404s or silently truncated pages. Apply schema/schema.graphql to DGraph; "+
		"see deploy/README.md step 8",
		"path", schemaPath, "missing_count", len(missing), "missing", strings.Join(missing, " | "))
}

// StartCheck runs CheckOnce in the background so a slow or unreachable DGraph
// cannot delay startup.
func StartCheck(ctx context.Context, adminURL, schemaPath string, logger *slog.Logger) {
	go func() {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		CheckOnce(cctx, adminURL, schemaPath, logger)
	}()
}
