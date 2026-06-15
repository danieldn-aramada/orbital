// Package startupcheck provides startup-time validation of configured
// downstream URLs. Used by orbital (bundler URLs) and orb (consumer URLs) to
// fail fast when a URL is configured but unreachable — preventing silent
// "publish ran but skipped bundler" / "import ran but dispatched to no one"
// scenarios that drop information without an obvious error path.
package startupcheck

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ProbeOrFatal probes each configured URL with a short timeout. Returns:
//   - (true, nil)   — at least one URL was set; all reachable; safe to start.
//   - (false, nil)  — no URLs configured; caller decides whether to continue
//                     (typically yes, with a loud warning logged here).
//   - (_, err)      — URLs were configured but at least one unreachable; the
//                     caller should treat this as fatal and exit IF strict=true.
//
// Each URL is probed with HTTP GET; any 2xx/3xx/4xx response counts as "the
// service exists and is responding." Only connection-level failures and 5xx
// responses count as unreachable. This is deliberate — many bundler/consumer
// endpoints return 405 to GET (POST-only handlers), which still means
// "service is alive."
//
// strict controls behavior on unreachable URLs:
//   - true  (production)  — return the error so caller fails startup.
//   - false (dev)         — log a WARN and return (true, nil). Devs running
//                           `make run-orbital` without yet starting cb-bundler
//                           shouldn't be blocked; first publish will still
//                           surface the misconfig in the response.
func ProbeOrFatal(ctx context.Context, name string, urls []string, strict bool, logger *slog.Logger) (bool, error) {
	if len(urls) == 0 {
		logger.Warn(name+" not configured — feature disabled",
			"hint", "set the env var to enable; absence is silent",
			"affected", name+" pipeline will not run")
		return false, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 5 * time.Second}
	var unreachable []string
	for _, url := range urls {
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
		if err != nil {
			unreachable = append(unreachable, fmt.Sprintf("%s: build request: %v", url, err))
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			unreachable = append(unreachable, fmt.Sprintf("%s: %v", url, err))
			continue
		}
		_ = resp.Body.Close()
		// 5xx means the service is up but broken — count as unreachable for startup purposes.
		if resp.StatusCode >= 500 {
			unreachable = append(unreachable, fmt.Sprintf("%s: HTTP %d", url, resp.StatusCode))
			continue
		}
		logger.Info(name+" reachable", "url", url, "status", resp.StatusCode)
	}

	if len(unreachable) > 0 {
		msg := fmt.Sprintf("%s configured but unreachable:\n  %s",
			name, strings.Join(unreachable, "\n  "))
		if !strict {
			logger.Warn(name+" preflight failed (non-strict: continuing)",
				"detail", msg,
				"hint", "first publish/import will surface the misconfig in the response")
			return true, nil
		}
		return true, fmt.Errorf("%s", msg)
	}
	return true, nil
}
