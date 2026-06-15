package orb

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/armada/orbital/internal/orbconfig"
)

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

// NewDispatcher creates a Dispatcher from the given consumer registrations.
func NewDispatcher(consumers []orbconfig.ConsumerConfig) *Dispatcher {
	return &Dispatcher{
		consumers: consumers,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Dispatch broadcasts each non-graph layer to every registered consumer.
// One DispatchResult per layer (consumer responses collapsed: first 2xx wins;
// else the last 4xx/error). Layer-arrival ordering is stable — layers are
// sorted by media type so the manifest layer (lexicographically earlier than
// "mapping") is dispatched first, matching cb-controller's expectation that
// the ConfigBundle CR exists before the mapping arrives.
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
	// Retry on 409 Conflict: some consumers (e.g. cb-controller's mapping
	// handler) return 409 when prerequisite state hasn't landed yet (the
	// manifest layer creates the ConfigBundle CR via async SSA; the mapping
	// layer's OwnerReference write needs that CR to exist). Brief retry
	// covers the race window. Other status codes are returned as-is.
	const (
		maxAttempts = 4
		retryDelay  = 500 * time.Millisecond
	)
	var result DispatchResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result = d.postOnce(ctx, c, mediaType, data, tag, digest, importID)
		if result.StatusCode != http.StatusConflict {
			return result
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return result
			case <-time.After(retryDelay):
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
		result.Error = fmt.Sprintf("consumer %s returned %d", c.Name, resp.StatusCode)
	}
	return result
}
