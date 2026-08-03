package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ImageMeta holds the registry-side timestamps for a tag, read from a Zot
// registry's search extension (GraphQL at /v2/_zot/ext/search).
//
//   - PushTimestamp: when THIS registry received/wrote the image. For a Zot
//     configured as a sync mirror, that is the moment it mirrored the tag from
//     upstream (e.g. ACR) — the edge-landing time.
//   - LastUpdated: the image's build/content time (from the OCI image config
//     `created`). Set by the builder upstream — NOT the registry-receive time.
//
// Any field the registry did not return is the zero time.
type ImageMeta struct {
	PushTimestamp time.Time
	LastUpdated   time.Time
}

// GetImageMeta queries the Zot search extension for a tag's timestamps.
//
// Best-effort by contract: a registry without the search extension (or a
// non-Zot registry) yields an error the caller is expected to log and continue
// past — propagation metrics are simply not recorded for that import. Never
// fail an import because this call failed.
func GetImageMeta(ctx context.Context, cfg PullConfig, tag string) (ImageMeta, error) {
	scheme := "https"
	if cfg.AllowHTTP {
		scheme = "http"
	}
	// The search extension keys on the full "repo:tag" reference.
	image := cfg.Repo + ":" + tag
	gql := fmt.Sprintf(`{ Image(image: %q) { LastUpdated PushTimestamp } }`, image)
	reqBody, err := json.Marshal(map[string]string{"query": gql})
	if err != nil {
		return ImageMeta{}, fmt.Errorf("marshal query: %w", err)
	}

	url := fmt.Sprintf("%s://%s/v2/_zot/ext/search", scheme, cfg.Registry)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return ImageMeta{}, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Username != "" || cfg.Password != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ImageMeta{}, fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ImageMeta{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ImageMeta{}, fmt.Errorf("search returned %d: %s", resp.StatusCode, raw)
	}
	return parseImageMeta(raw)
}

// parseImageMeta decodes a Zot search-ext GraphQL response for the Image query.
// Split from the HTTP path so it is unit-testable without a live registry.
func parseImageMeta(raw []byte) (ImageMeta, error) {
	var payload struct {
		Data struct {
			Image struct {
				LastUpdated   string `json:"LastUpdated"`
				PushTimestamp string `json:"PushTimestamp"`
			} `json:"Image"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ImageMeta{}, fmt.Errorf("decode search response: %w", err)
	}
	if len(payload.Errors) > 0 {
		return ImageMeta{}, fmt.Errorf("search error: %s", payload.Errors[0].Message)
	}
	return ImageMeta{
		PushTimestamp: parseRegistryTime(payload.Data.Image.PushTimestamp),
		LastUpdated:   parseRegistryTime(payload.Data.Image.LastUpdated),
	}, nil
}

// parseRegistryTime tolerantly parses an RFC3339(/Nano) timestamp, returning the
// zero time for empty, unparseable, or zero-sentinel input. Zot serializes an
// unset timestamp as Go's zero time ("0001-01-01T00:00:00Z"), which callers
// treat as "unknown" — collapse it to the real zero value here.
func parseRegistryTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t.UTC()
}
