package oci

import "github.com/armada/orbital/internal/ocitype"

// ArtifactLayer records metadata for a single OCI layer pushed as part of a registry artifact.
//
// Layer media types are opaque from orbital's perspective. Consumers must NOT interpret
// them or build media-type-aware UI features. See feedback_orb_orbital_agnostic_of_configbundle
// memory for the principle this enforces.
//
// The canonical definition lives in internal/ocitype to avoid import cycles with ent/schema.
type ArtifactLayer = ocitype.ArtifactLayer
