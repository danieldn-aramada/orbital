package divergence

import "strings"

// S3 path convention for divergence reports — shared between the writer
// (orb's Publisher, this package) and the reader (orbital's divergenceingest
// package). Keeping the encode/decode logic in one file means a change to the
// path format requires a change in one place; producer and consumer can't drift.
//
// Format: divergence/<repoPath>/report.json
//
// Overwrite-in-place at a stable key — the Terraform-state pattern. Each publish
// PUTs the same key, so the bucket stays at O(1) objects per DC regardless of
// publish cadence. Orbital's ingester lists the prefix, picks the sole key, and
// reads the body; its idempotency cursor uses `publishedAt` from the report body,
// not the key name, so overwrite is transparent to the ingest path.
//
// repoPath is the OCI repo path WITHOUT the registry host
// (e.g. "orbital/colo-galleon", not "localhost:5001/orbital/colo-galleon").
// Orb's config (ORB_OCI_REPO) is already in this form. Orbital's
// RegistryArtifact.Repository stores the full registry-prefixed form for
// display, so the ingester strips the host via NormalizeRepoPath before
// listing under this prefix.

const (
	// PrefixRoot is the top-level S3/blob prefix for divergence reports.
	// Trailing slash included so direct concatenation works.
	PrefixRoot = "divergence/"

	// ReportFilename is the fixed filename every publish writes to. Stable key =
	// overwrite semantics; no timestamped keys accumulate in the bucket.
	ReportFilename = "report.json"
)

// ReportKey builds the canonical S3/blob key for a given OCI repo path.
// The key is stable per repoPath — repeated publishes overwrite in place.
func ReportKey(repoPath string) string {
	return PrefixRoot + repoPath + "/" + ReportFilename
}

// PrefixForRepo returns the listing prefix orbital's ingester uses to discover
// reports for a given repo path.
func PrefixForRepo(repoPath string) string {
	return PrefixRoot + repoPath + "/"
}

// NormalizeRepoPath strips the leading registry host segment from a stored
// Repository string (e.g. "localhost:5001/orbital/colo-galleon" → "orbital/colo-galleon").
// Returns the input unchanged if there is no '/' (defensive — should never happen
// for valid Repository values).
func NormalizeRepoPath(repository string) string {
	if idx := strings.Index(repository, "/"); idx >= 0 {
		return repository[idx+1:]
	}
	return repository
}
