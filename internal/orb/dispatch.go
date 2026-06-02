package orb

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/armada/orbital/internal/orbconfig"
)

// DispatchResult records the outcome of dispatching one layer to one consumer.
type DispatchResult struct {
	MediaType  string `json:"mediaType"`
	URL        string `json:"url"`
	StatusCode int    `json:"statusCode,omitempty"`
	Error      string `json:"error,omitempty"`
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

// Dispatch sends each extra layer to its registered consumer.
// Dispatch is best-effort: each consumer result is recorded individually.
// A failed dispatch does not stop other consumers from receiving their layer.
// Layers with no registered consumer are silently skipped.
func (d *Dispatcher) Dispatch(ctx context.Context, layers map[string][]byte, tag, digest, importID string) []DispatchResult {
	var results []DispatchResult
	for _, c := range d.consumers {
		data, ok := layers[c.MediaType]
		if !ok {
			continue
		}
		results = append(results, d.dispatchOne(ctx, c, data, tag, digest, importID))
	}
	return results
}

func (d *Dispatcher) dispatchOne(ctx context.Context, c orbconfig.ConsumerConfig, data []byte, tag, digest, importID string) DispatchResult {
	result := DispatchResult{MediaType: c.MediaType, URL: c.URL}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(data))
	if err != nil {
		result.Error = fmt.Sprintf("create request: %s", err)
		return result
	}
	req.Header.Set("Content-Type", c.MediaType)
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
		result.Error = fmt.Sprintf("consumer returned %d", resp.StatusCode)
	}
	return result
}
