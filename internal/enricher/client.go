package enricher

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Request is the payload Orbital sends to an enricher before pushing an OCI artifact.
// The enricher uses jobId and datacenter to query Orbital's GraphQL for whatever data it needs.
type Request struct {
	JobID      string `json:"jobId"`
	Datacenter string `json:"datacenter"`
}

// Layer is an additional OCI artifact layer returned by an enricher.
type Layer struct {
	MediaType string `json:"mediaType"`
	Data      []byte `json:"-"`
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

// Client calls a single enricher endpoint.
//
// The backing HTTP client is configurable via WithHTTPClient — pass a
// *retryablehttp.Client.StandardClient() for retry/backoff behaviour in production.
// The default is a plain net/http.Client with the given timeout.
type Client struct {
	url            string
	httpClient     *http.Client
	maxResponseBytes int64
}

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

// WithMaxResponseBytes sets the maximum number of bytes read from an enricher response.
// Responses exceeding this limit are rejected. Default: 10 MB.
func WithMaxResponseBytes(n int64) ClientOption {
	return func(c *Client) *Client {
		c.maxResponseBytes = n
		return c
	}
}

// New creates a Client for the given enricher URL.
// The default HTTP client has the given timeout and a 10 MB response size cap.
//
// Options:
//
//	WithHTTPClient() to replace the backing HTTP client (default net/http.Client)
//	WithMaxResponseBytes() to change the response size limit (default 10 MB)
func New(url string, timeout time.Duration, opts ...ClientOption) *Client {
	c := &Client{
		url:              url,
		httpClient:       &http.Client{Timeout: timeout},
		maxResponseBytes: 10 * 1024 * 1024, // 10 MB
	}
	for _, opt := range opts {
		c = opt(c)
	}
	return c
}

// Enrich calls the enricher and returns the additional OCI layers to include in the artifact.
func (c *Client) Enrich(ctx context.Context, req Request) ([]Layer, error) {
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
		return nil, fmt.Errorf("call enricher: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("enricher returned HTTP %d", resp.StatusCode)
	}

	// Read up to maxResponseBytes+1 so we can detect an oversize response.
	limited, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read enricher response: %w", err)
	}
	if int64(len(limited)) > c.maxResponseBytes {
		return nil, fmt.Errorf("enricher response exceeds %d byte limit", c.maxResponseBytes)
	}

	var layers []Layer
	if err := json.Unmarshal(limited, &layers); err != nil {
		return nil, fmt.Errorf("decode enricher response: %w", err)
	}
	return layers, nil
}
