package divergence

import (
	"fmt"
	"strings"
)

// S3 path convention for divergence snapshots — shared between the writer
// (orb's Publisher, this package) and the reader (orbital's divergenceingest
// package). Keeping the encode/decode logic in one file means a change to the
// path format requires a change in one place; producer and consumer can't drift.
//
// Format: divergence/<repoPath>/<RFC3339-with-colons-replaced>.json
//
// repoPath is the OCI repo path WITHOUT the registry host
// (e.g. "orbital/colo-galleon", not "localhost:5001/orbital/colo-galleon").
// Orb's config (ORB_OCI_REPO) is already in this form. Orbital's
// RegistryArtifact.Repository stores the full registry-prefixed form for
// display, so the ingester strips the host via NormalizeRepoPath before
// listing under this prefix.

const (
	// PrefixRoot is the top-level S3/blob prefix for divergence snapshots.
	// Trailing slash included so direct concatenation works.
	PrefixRoot = "divergence/"

	// Suffix is the file extension for snapshot files.
	Suffix = ".json"
)

// SnapshotKey builds the canonical S3/blob key for a given OCI repo path and
// timestamp string. The timestamp must already be in the RFC3339-colon-replaced
// format (callers can use TimestampForKey).
func SnapshotKey(repoPath, ts string) string {
	return PrefixRoot + repoPath + "/" + ts + Suffix
}

// PrefixForRepo returns the listing prefix orbital's ingester uses to discover
// snapshots for a given repo path.
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

// TimestampForKey formats a UTC timestamp into the colon-stripped form used in
// snapshot filenames. Lexicographic ordering of these filenames matches
// chronological order, which the ingester relies on to pick the latest key.
func TimestampForKey(year int, month, day, hour, min, sec int) string {
	return fmt.Sprintf("%04d-%02d-%02dT%02d-%02d-%02dZ",
		year, int(month), day, hour, min, sec)
}
