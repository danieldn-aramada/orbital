// Package ocitype defines shared OCI metadata types used by both the ent schema
// and the oci publisher. It must not import ent or oci to avoid import cycles.
package ocitype

// ArtifactLayer records metadata for a single OCI layer pushed as part of a registry artifact.
//
// Layer media types are opaque from orbital's perspective. Consumers must NOT interpret
// them or build media-type-aware UI features. See feedback_orb_orbital_agnostic_of_configbundle
// memory for the principle this enforces.
type ArtifactLayer struct {
	MediaType       string `json:"mediaType"`
	SizeBytes       int64  `json:"sizeBytes"`
	Digest          string `json:"digest,omitempty"`
	IsOrbitalNative bool   `json:"isOrbitalNative"` // true for data.json.gz + schema.gz; false for anything bundler-added
	// Producer is the friendly name of whatever produced this layer (e.g.
	// "orbital" for the two graph layers, "configbundle-bundler" for layers
	// returned by that bundler). Mirrors the OCI manifest annotation
	// `com.armada.orbital.producer` written at push time. Empty for legacy
	// artifacts published before producer attribution was introduced.
	Producer string `json:"producer,omitempty"`
}

// AnnotationProducer is the OCI manifest annotation key that carries the
// friendly producer name for each layer. Consumers (orb's UI) read this to
// attribute layers to specific producers.
const AnnotationProducer = "com.armada.orbital.producer"

// ProducerOrbital is the canonical producer name for orbital's own graph
// layers (subgraph data + schema).
const ProducerOrbital = "orbital"
