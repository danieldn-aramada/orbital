package orb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/armada/orbital/internal/orbconfig"
)

// maxConsumerErrorBytes caps how much of a non-2xx response body we read
// into the DispatchResult.Error field. Consumers (cb-controller, others)
// should return concise diagnostic messages; if they return a huge stack
// trace we don't want to balloon the import history JSON. The cap is large
// enough to fit a K8s status response with reason + message + details.
const maxConsumerErrorBytes = 4096

// DispatchResult records the outcome of dispatching one layer.
//
// With the consumer-centric `ORB_CONSUMERS` schema, orb broadcasts each
// layer to every registered consumer with `Content-Type: <media type>`.
// Consumers route internally and return 415 for types they don't handle.
// The DispatchResult below collapses that fan-out per layer: the first
// 2xx response wins (URL+StatusCode), and if no consumer accepted, the
// last 4xx/error is surfaced for diagnostics.
type DispatchResult struct {
	MediaType    string `json:"mediaType"`
	ConsumerName string `json:"consumerName,omitempty"` // name of the accepting consumer; empty if no 2xx
	URL          string `json:"url"`
	StatusCode   int    `json:"statusCode,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Dispatcher sends artifact layers to registered consumers.
type Dispatcher struct {
	consumers []orbconfig.ConsumerConfig
	client    *http.Client
}

// NewDispatcher creates a Dispatcher with exponential-backoff retry on
// transient consumer errors.
//
// Why retries: consumer dispatch can race with downstream state that hasn't
// settled yet (e.g. cb-controller's SSA-apply returns 2xx before the K8s
// informer observes the resulting resource — a same-import follow-up write
// can hit a transient 409 while that window is open). Retry lets these
// transient conditions self-resolve without operator intervention.
//
// Retry policy: 5 attempts with exponential backoff (500ms doubling, capped
// at 2s) — waits between attempts 500ms, 1s, 2s, 2s = ~5.5s total budget.
// Retries fire on 409, 5xx, 429, and transport errors. Other 4xx (real
// client errors) fail immediately. Hand-rolled rather than go-retryablehttp
// because the library eats the final response body on exhausted retries,
// dropping the StatusCode signal the DispatchResult relies on.
func NewDispatcher(consumers []orbconfig.ConsumerConfig) *Dispatcher {
	return &Dispatcher{
		consumers: consumers,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// shouldRetry returns true when the response or error indicates the request
// might succeed on a future attempt. Domain-specific to dispatch.
func shouldRetry(result DispatchResult) bool {
	if result.Error != "" && result.StatusCode == 0 {
		return true // transport-layer error (connection refused, timeout, etc.)
	}
	switch result.StatusCode {
	case http.StatusConflict, // 409 — cb-controller SSA-observe race
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

// backoffDelay returns the wait between attempt N and attempt N+1.
// 500ms doubling, capped at 2s — appropriate for K8s informer sync latency.
// Attempts: 1, 2, 3, 4, 5 → waits: 500ms, 1s, 2s, 2s = 5.5s total budget.
func backoffDelay(attempt int) time.Duration {
	delay := 500 * time.Millisecond
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > 2*time.Second {
			return 2 * time.Second
		}
	}
	return delay
}

// Dispatch broadcasts each non-graph layer to every registered consumer.
// One DispatchResult per layer (consumer responses collapsed: first 2xx wins;
// else the last 4xx/error). Layer-arrival ordering is stable — layers are
// sorted by media type before dispatch, so multi-layer bundles arrive in a
// deterministic order per import.
func (d *Dispatcher) Dispatch(ctx context.Context, layers map[string][]byte, tag, digest, importID string) []DispatchResult {
	if len(d.consumers) == 0 || len(layers) == 0 {
		return nil
	}
	mediaTypes := make([]string, 0, len(layers))
	for mt := range layers {
		mediaTypes = append(mediaTypes, mt)
	}
	sort.Strings(mediaTypes)

	results := make([]DispatchResult, 0, len(mediaTypes))
	for _, mt := range mediaTypes {
		results = append(results, d.dispatchLayer(ctx, mt, layers[mt], tag, digest, importID))
	}
	return results
}

// dispatchLayer POSTs one layer to every consumer (each gets the same bytes
// + `Content-Type: <media type>`) and collapses the responses into a single
// result. Consumers that 415 are not surfaced — that's expected when a
// consumer doesn't care about this media type. The first 2xx becomes the
// canonical result. If no 2xx, the last non-415 status / error is returned
// so the operator can see what went wrong.
func (d *Dispatcher) dispatchLayer(ctx context.Context, mediaType string, data []byte, tag, digest, importID string) DispatchResult {
	var last DispatchResult
	last.MediaType = mediaType
	for _, c := range d.consumers {
		r := d.dispatchToConsumer(ctx, c, mediaType, data, tag, digest, importID)
		// First 2xx wins.
		if r.StatusCode >= 200 && r.StatusCode < 300 {
			return r
		}
		// Skip 415 in the "last" surface — that's the consumer correctly
		// declining a media type it doesn't handle. Only surface real errors.
		if r.StatusCode != http.StatusUnsupportedMediaType {
			last = r
		}
	}
	return last
}

func (d *Dispatcher) dispatchToConsumer(ctx context.Context, c orbconfig.ConsumerConfig, mediaType string, data []byte, tag, digest, importID string) DispatchResult {
	const maxAttempts = 5
	var result DispatchResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result = d.postOnce(ctx, c, mediaType, data, tag, digest, importID)
		if !shouldRetry(result) {
			return result
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return result
			case <-time.After(backoffDelay(attempt)):
			}
		}
	}
	return result
}

func (d *Dispatcher) postOnce(ctx context.Context, c orbconfig.ConsumerConfig, mediaType string, data []byte, tag, digest, importID string) DispatchResult {
	result := DispatchResult{MediaType: mediaType, ConsumerName: c.Name, URL: c.URL}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(data))
	if err != nil {
		result.Error = fmt.Sprintf("create request: %s", err)
		return result
	}
	req.Header.Set("Content-Type", mediaType)
	req.Header.Set("X-Orb-Tag", tag)
	req.Header.Set("X-Orb-Digest", digest)
	req.Header.Set("X-Orb-Import-ID", importID)

	resp, err := d.client.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("dispatch: %s", err)
		return result
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	if resp.StatusCode >= 400 {
		// Read up to N bytes of the response body so the operator can see
		// WHY the consumer rejected the layer. Without this the error
		// surface is just "consumer X returned 409" with no context — which
		// forces the operator to dig into the consumer's logs. Bodies are
		// truncated; if the consumer returns more, we mark it with an
		// ellipsis. Bare reads can fail (closed connection, etc.); on
		// failure we still surface the status code with a status-only
		// message.
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxConsumerErrorBytes))
		bodyStr := string(bytes.TrimSpace(body))
		if readErr != nil || bodyStr == "" {
			result.Error = fmt.Sprintf("consumer %s returned %d", c.Name, resp.StatusCode)
		} else {
			truncated := ""
			if len(body) == maxConsumerErrorBytes {
				truncated = " (truncated)"
			}
			result.Error = fmt.Sprintf("consumer %s returned %d: %s%s", c.Name, resp.StatusCode, bodyStr, truncated)
		}
	}
	return result
}
