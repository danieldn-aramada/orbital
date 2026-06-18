package bundler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ParseSpec splits an `ORBITAL_BUNDLER_URLS` entry into a (name, url) pair.
// Accepts both `name=url` (canonical) and bare URLs (back-compat; the name
// falls back to "bundler"). The friendly name lands in OCI layer annotations
// (`com.armada.orbital.producer`) so orb's UI can attribute layers to specific
// producers.
//
// TODO(post-MVP): the `name=url` env DSL is custom and brittle. This parser
// has already caused one bug (preflight bypassed it and probed the raw
// `name=url` string as a URL). Refactor to either one env var per bundler
// (`ORBITAL_BUNDLER_<NAME>=url`) or a ConfigMap-mounted YAML/JSON file. See
// ROADMAP.md "Refactor bundler URL config away from name=url env DSL".
func ParseSpec(spec string) (name, url string) {
	if i := strings.Index(spec, "="); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return "bundler", spec
}

// Request is the payload Orbital sends to a bundler before pushing an OCI artifact.
// OrbID is the canonical DataCenter identifier — bundlers query Orbital's
// GraphQL via this exact-match key (hash-indexed in DGraph, supports `eq`
// filters). Orbital always has an orbId for completed exports.
type Request struct {
	OrbID string `json:"orbId"`
}

// Layer is an additional OCI artifact layer returned by a bundler.
//
// Producer is NOT part of the wire shape — the bundler doesn't self-identify.
// Orbital sets it after decoding using the configured friendly name for the
// bundler client that produced this response. Surfaces as the OCI manifest
// annotation `com.armada.orbital.producer` at push time.
type Layer struct {
	MediaType string `json:"mediaType"`
	Data      []byte `json:"-"`
	Producer  string `json:"-"` // set by orbital post-decode; never serialized
}

func (l *Layer) UnmarshalJSON(b []byte) error {
	var raw struct {
		MediaType string `json:"mediaType"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.DecodeString(raw.Data)
	if err != nil {
		return fmt.Errorf("decode layer data: %w", err)
	}
	l.MediaType = raw.MediaType
	l.Data = decoded
	return nil
}

// Result is the bundler's response envelope. Layers go into the OCI artifact;
// ConsumedResolutionIDs identify pending force-resolutions that this bundler
// surfaced as `spec.takeover[]` entries so orbital can mark them consumed
// after the push succeeds.
type Result struct {
	Layers                []Layer
	ConsumedResolutionIDs []string
}

// Client calls a single bundler endpoint.
//
// The backing HTTP client is configurable via WithHTTPClient — pass a
// *retryablehttp.Client.StandardClient() for retry/backoff behaviour in production.
// The default is a plain net/http.Client with the given timeout.
type Client struct {
	name           string // friendly producer name; surfaces in OCI layer annotations
	url            string
	httpClient     *http.Client
	maxResponseBytes int64
}

// Name returns the friendly producer name configured for this bundler. Stamped
// onto OCI layer annotations so consumers (orb's UI) can attribute each layer
// to a specific producer instead of generic "bundler".
func (c *Client) Name() string { return c.name }

// ClientOption configures a Client.
type ClientOption func(*Client) *Client

// WithHTTPClient replaces the backing HTTP client.
// Use this to inject a retryable client (e.g. go-retryablehttp) or a test double.
// If using a custom client, ensure timeouts and CA certificates are configured appropriately.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) *Client {
		c.httpClient = client
		return c
	}
}

// WithMaxResponseBytes sets the maximum number of bytes read from a bundler response.
// Responses exceeding this limit are rejected. Default: 10 MB.
func WithMaxResponseBytes(n int64) ClientOption {
	return func(c *Client) *Client {
		c.maxResponseBytes = n
		return c
	}
}

// New creates a Client for the given bundler URL.
// The default HTTP client has the given timeout and a 10 MB response size cap.
// `name` is the friendly producer label surfaced in OCI layer annotations.
//
// Options:
//
//	WithHTTPClient() to replace the backing HTTP client (default net/http.Client)
//	WithMaxResponseBytes() to change the response size limit (default 10 MB)
func New(name, url string, timeout time.Duration, opts ...ClientOption) *Client {
	c := &Client{
		name:             name,
		url:              url,
		httpClient:       &http.Client{Timeout: timeout},
		maxResponseBytes: 10 * 1024 * 1024, // 10 MB
	}
	for _, opt := range opts {
		c = opt(c)
	}
	return c
}

// Enrich calls the bundler and returns the layers (plus any consumed
// force-resolution IDs the bundler reports) to include in the artifact.
func (c *Client) Enrich(ctx context.Context, req Request) (*Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call bundler: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read the response body so operators see the actionable error from the
		// bundler (e.g., "graphql error: Field 'eq' is not defined...") instead
		// of just "HTTP 500" with nothing to act on.
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes))
		msg := strings.TrimSpace(string(bodyBytes))
		if msg == "" {
			return nil, fmt.Errorf("bundler returned HTTP %d (empty response)", resp.StatusCode)
		}
		return nil, fmt.Errorf("bundler returned HTTP %d: %s", resp.StatusCode, msg)
	}

	// Read up to maxResponseBytes+1 so we can detect an oversize response.
	limited, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read bundler response: %w", err)
	}
	if int64(len(limited)) > c.maxResponseBytes {
		return nil, fmt.Errorf("bundler response exceeds %d byte limit", c.maxResponseBytes)
	}

	// Wire envelope from cb-bundler (and any future bundler): a JSON object
	// with `layers` and optional `consumedResolutionIds`.
	var envelope struct {
		Layers                []Layer  `json:"layers"`
		ConsumedResolutionIDs []string `json:"consumedResolutionIds"`
	}
	if err := json.Unmarshal(limited, &envelope); err != nil {
		return nil, fmt.Errorf("decode bundler response: %w", err)
	}
	return &Result{
		Layers:                envelope.Layers,
		ConsumedResolutionIDs: envelope.ConsumedResolutionIDs,
	}, nil
}
