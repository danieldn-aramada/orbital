// Package configitems is orbital's single source of truth for the set of
// ConfigItem types declared in schema/schema.graphql.
//
// Background — why this exists
//
// Adding a ConfigItem to orbital used to touch ~13 places: GraphQL queries,
// before-fetch maps, audit allowlist regexes, related-orbId walkers, cascade
// delete queries, JS modal snapshots, mutation shapes, response selections.
// Each of those layers maintained its OWN list of types, and any miss was
// silent: a mutation would succeed but produce zero audit events, or an audit
// row would render with no diff, or a delete would orphan children.
//
// This registry centralizes the wiring metadata. Every other layer DERIVES
// from this single declaration. Adding a new ConfigItem now requires:
//   1. Declare it in schema/schema.graphql
//   2. Add a Type entry below
// That's it. The Go audit pipeline, before-fetcher, and (via the
// configitem-editor JS module) the front-end editor all pick it up.
//
// Boundaries
//
// This registry describes only ConfigItem-shaped types — entities with an
// orbId and a place in the parent/child relationship graph. It does NOT
// describe arbitrary GraphQL types, scalar enums, or query-only interfaces.
// `IsRoot` distinguishes parent-page types (DataCenter, Server,
// EksaKubernetesCluster) from owned-only sub-resources (IdracSettings, etc.).
package configitems

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Type describes one ConfigItem from the orbital schema.
type Type struct {
	// Name is the GraphQL type name as declared in schema.graphql.
	// Used as the cross-layer key (audit operation suffix, response payload
	// field name prefix, etc.).
	Name string

	// IsRoot indicates a type that has its own page in the UI (DataCenter,
	// Server, EksaKubernetesCluster). Non-root types are owned-only
	// sub-resources rendered inside their parent's edit modal.
	IsRoot bool

	// OwnerType is the GraphQL type that owns this one — the parent in the
	// composition hierarchy. Empty for top-level standalone types.
	// E.g. EtcdBackup.OwnerType = "ClusterBackup".
	OwnerType string

	// OwnerField is the field on this type that points back to the owner.
	// E.g. EtcdBackup.OwnerField = "clusterBackupEtcd" (the @hasInverse pointer).
	// Used by mutations that need to link a new child to its parent.
	OwnerField string

	// ChildField is the field on the OwnerType that points DOWN at this one.
	// E.g. EtcdBackup.ChildField = "etcd" (because ClusterBackup.etcd points
	// at EtcdBackup). Used by JS editor subtree paths.
	ChildField string

	// BeforeFields is the GraphQL selection used to fetch the before-state for
	// audit diff rendering. Must include `id orbId name version` at minimum
	// plus every editable scalar so buildDiffHTML has both sides to compare.
	BeforeFields string

	// FormFields are the scalar/Boolean fields the JS editor exposes for
	// user editing. The editor reads these on snapshot-build, on diff at
	// submit, and on building the `set` map. Excludes audit/metadata fields
	// (id, orbId, version, namespace, createdBy/At, updatedBy/At).
	FormFields []string

	// PayloadField is the response selection field on Add{Type}Payload that
	// returns the affected row (so the audit extractor can find the orbId in
	// the response body). DGraph's convention is the lowercase type name
	// pluralized as a list — but it doesn't always pluralize cleanly
	// (S3Sync's payload is "s3Sync"), so we declare it explicitly.
	PayloadField string

	// Implements lists GraphQL interface names this type implements. Used by
	// Children() to walk interface-typed ownership: backup sub-kinds declare
	// OwnerType: "KubernetesCluster" (the interface), and Children() returns
	// them when asked about EksaKubernetesCluster because EksaKubernetesCluster
	// implements KubernetesCluster. Schema source of truth.
	Implements []string

	// JSONStringFields lists FormFields whose schema type is `String` but
	// whose VALUE is JSON. The page handler parses these before injecting
	// into the JSON editor (so they display as nested structure); on submit,
	// configitem-editor.js MUST stringify them again before they go into the
	// mutation's `set`, or DGraph rejects with "cannot use as String".
	// E.g. DataCenter.assetDataV2 — declared `String # json` in the schema.
	JSONStringFields []string
}

// Types is THE registry. Adding a new ConfigItem starts here.
//
// Ordering convention: parents before children; types within a family grouped
// together. Order doesn't matter for derived values but keeps the file
// readable when grepping.
var Types = []Type{
	// ── Inventory hierarchy ──────────────────────────────────────────────────
	{
		Name:             "DataCenter",
		IsRoot:           true,
		BeforeFields:     "id orbId name version assetDataV2",
		FormFields:       []string{"name", "assetDataV2"},
		JSONStringFields: []string{"assetDataV2"},
		PayloadField:     "dataCenter",
	},
	{
		Name:         "Rack",
		OwnerType:    "DataCenter",
		ChildField:   "racks",
		BeforeFields: "id orbId name version",
		FormFields:   []string{"name"},
		PayloadField: "rack",
	},
	{
		Name:         "Server",
		IsRoot:       true,
		BeforeFields: "id orbId name version hostname model manufacturer serviceTag rackPosition oobMAC idracSettings { firmwareVersion sshEnabled ipmiEnabled lockdownModeEnabled osToIdracPassThroughEnabled usbManagementPortEnabled dhcpEnabled racadmEnabled }",
		FormFields:   []string{"hostname", "manufacturer", "model", "oobMAC", "rackPosition", "serviceTag"},
		PayloadField: "server",
	},
	{
		Name:         "IdracSettings",
		OwnerType:    "Server",
		OwnerField:   "server",
		ChildField:   "idracSettings",
		BeforeFields: "id orbId name version firmwareVersion sshEnabled ipmiEnabled lockdownModeEnabled osToIdracPassThroughEnabled usbManagementPortEnabled dhcpEnabled racadmEnabled",
		FormFields:   []string{"firmwareVersion", "sshEnabled", "ipmiEnabled", "lockdownModeEnabled", "osToIdracPassThroughEnabled", "usbManagementPortEnabled", "dhcpEnabled", "racadmEnabled"},
		PayloadField: "idracSettings",
	},
	{
		Name:         "ServerConfigurationProfile",
		OwnerType:    "Server",
		OwnerField:   "server",
		ChildField:   "serverConfigurationProfile",
		BeforeFields: "id orbId name version",
		PayloadField: "serverConfigurationProfile",
	},
	{
		Name:         "StorageController",
		OwnerType:    "Server",
		OwnerField:   "server",
		ChildField:   "storageControllers",
		BeforeFields: "id orbId name version",
		PayloadField: "storageController",
	},
	{
		Name:         "StorageDevice",
		OwnerType:    "StorageController",
		OwnerField:   "storageController",
		ChildField:   "storageDevices",
		BeforeFields: "id orbId name version",
		PayloadField: "storageDevice",
	},
	{
		Name:         "StorageVolume",
		OwnerType:    "StorageDevice",
		ChildField:   "storageVolumes",
		BeforeFields: "id orbId name version",
		PayloadField: "storageVolume",
	},

	// ── IP addresses (referenced by many types via @hasInverse) ──────────────
	{
		Name:         "IPAddress",
		BeforeFields: "id orbId name version address type role",
		PayloadField: "ipAddress",
	},

	// ── Kubernetes cluster hierarchy ─────────────────────────────────────────
	{
		// KubernetesCluster is an INTERFACE; the entry covers interface-level
		// mutations (updateKubernetesCluster/deleteKubernetesCluster) that
		// touch only universal fields. Concrete cluster types have their own
		// entries below.
		Name:         "KubernetesCluster",
		BeforeFields: "id orbId name version kubernetesVersion cni environment",
	},
	{
		Name:         "EksaKubernetesCluster",
		IsRoot:       true,
		BeforeFields: "id orbId name version kubernetesVersion cni environment clusterType",
		FormFields:   []string{"kubernetesVersion", "cni", "environment", "clusterType"},
		PayloadField: "eksaKubernetesCluster",
		Implements:   []string{"KubernetesCluster"},
	},
	{
		Name:         "KubernetesNode",
		OwnerType:    "KubernetesCluster",
		OwnerField:   "cluster",
		ChildField:   "nodes",
		BeforeFields: "id orbId name version role",
		PayloadField: "kubernetesNode",
	},
	{
		Name:         "ClusterBackup",
		OwnerType:    "KubernetesCluster",
		OwnerField:   "cluster",
		ChildField:   "backup",
		BeforeFields: "id orbId name version",
		PayloadField: "clusterBackup",
	},
	{
		Name:         "EtcdBackup",
		OwnerType:    "ClusterBackup",
		OwnerField:   "clusterBackupEtcd",
		ChildField:   "etcd",
		BeforeFields: "id orbId name version enabled schedule location retentionDays",
		FormFields:   []string{"enabled", "schedule", "location", "retentionDays"},
		PayloadField: "etcdBackup",
	},
	{
		Name:         "VeleroBackup",
		OwnerType:    "ClusterBackup",
		OwnerField:   "clusterBackupVelero",
		ChildField:   "velero",
		BeforeFields: "id orbId name version enabled schedule location retentionDays",
		FormFields:   []string{"enabled", "schedule", "location", "retentionDays"},
		PayloadField: "veleroBackup",
	},
	{
		Name:         "S3Sync",
		OwnerType:    "ClusterBackup",
		OwnerField:   "clusterBackupS3Sync",
		ChildField:   "s3Sync",
		BeforeFields: "id orbId name version enabled",
		FormFields:   []string{"enabled"},
		PayloadField: "s3Sync",
	},
}

// nameSet caches the set of known type names for O(1) regex builder access.
var nameSet = func() map[string]Type {
	m := make(map[string]Type, len(Types))
	for _, t := range Types {
		m[t.Name] = t
	}
	return m
}()

// FindByName returns the Type entry for `name`, or false if not registered.
func FindByName(name string) (Type, bool) {
	t, ok := nameSet[name]
	return t, ok
}

// MustFindByName returns the Type entry or panics — for places where the
// caller is certain the type is registered (e.g. derived helpers called only
// for types already known to be in the registry).
func MustFindByName(name string) Type {
	t, ok := nameSet[name]
	if !ok {
		panic(fmt.Sprintf("configitems: type %q not registered", name))
	}
	return t
}

// Children returns the types that name `parent` as their OwnerType, including
// interface-typed ownership: if `parent` is a concrete type that implements
// an interface, children declaring the interface as their OwnerType are
// included too. E.g. Children("EksaKubernetesCluster") includes
// ClusterBackup/KubernetesNode (which declare OwnerType: "KubernetesCluster")
// because EksaKubernetesCluster.Implements ⊇ KubernetesCluster.
//
// Used by audit aggregation (collect related orbIds) and cascade delete.
func Children(parent string) []Type {
	parentT, parentKnown := nameSet[parent]
	out := make([]Type, 0)
	for _, t := range Types {
		if t.OwnerType == parent {
			out = append(out, t)
			continue
		}
		// Interface-typed ownership: if `parent` is concrete and implements
		// an interface `t` names as OwnerType, include `t`.
		if parentKnown && t.OwnerType != "" && slices.Contains(parentT.Implements, t.OwnerType) {
			out = append(out, t)
		}
	}
	return out
}

// KnownMutationsRegex returns a compiled regex matching every add/update/delete
// mutation against any registered type. Drop-in replacement for the previously
// hand-maintained `knownMutationRe` in `internal/handler/graphql.go`.
//
// The regex is built ONCE at first call from the registered Types — never
// hand-edit and never let the two drift apart.
func KnownMutationsRegex() *regexp.Regexp {
	return knownMutationsRegex
}

var knownMutationsRegex = func() *regexp.Regexp {
	names := make([]string, 0, len(Types))
	for _, t := range Types {
		names = append(names, regexp.QuoteMeta(t.Name))
	}
	pattern := `(?i)\b(add|update|delete)(` + strings.Join(names, "|") + `)\b`
	return regexp.MustCompile(pattern)
}()

// BeforeFields returns the GraphQL selection string the audit before-fetcher
// should use when querying the before-state of `typeName`. Returns "" if the
// type isn't registered or has no BeforeFields declared.
//
// Drop-in replacement for the previously hand-maintained `typeBeforeFields`
// map in `internal/handler/graphql.go`.
func BeforeFields(typeName string) string {
	t, ok := nameSet[typeName]
	if !ok {
		return ""
	}
	return t.BeforeFields
}

// EditTarget mirrors the shape consumed by web/shared/static/configitem-editor.js.
// One entry per editable entity in the JSON tree shown by a parent's edit
// modal: the parent itself + each owned child. Marshaled into the page as a
// JSON blob (`<script type="application/json" id="..-edit-targets-...">`)
// so the generic JS module can snapshot, diff, and dispatch update{Kind}
// mutations without page-specific JS.
type EditTarget struct {
	Path               []string     `json:"path"`
	Kind               string       `json:"kind"`
	OrbID              string       `json:"orbId"`
	Fields             []string     `json:"fields"`
	JSONStringFields   []string     `json:"jsonStringFields,omitempty"`
	PayloadField       string       `json:"payloadField"`
	Namespace          string       `json:"namespace"`
	ParentInverseField string       `json:"parentInverseField,omitempty"`
	ParentOrbID        string       `json:"parentOrbId,omitempty"`
	ParentWrapper      *EditWrapper `json:"parentWrapper,omitempty"`
}

// EditWrapper describes a structural parent ConfigItem that may need to be
// CREATED on first-time configure of any of its children (e.g. ClusterBackup
// before any of its EtcdBackup/VeleroBackup/S3Sync are added). The JS module
// emits a one-time link mutation on the root's `parentField` when needed.
type EditWrapper struct {
	Kind        string `json:"kind"`
	OrbID       string `json:"orbId"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	ParentField string `json:"parentField"` // field on the ROOT type that points at this wrapper (e.g. "backup")
}

// BuildEditTargets composes the targets list for a root entity's edit modal.
// Driven entirely by the registry — adding a new ConfigItem in `Types` above
// auto-propagates here, no per-page wiring.
//
// Parameters:
//   - rootType: the GraphQL type of the page's primary entity (e.g. "Server")
//   - rootOrbID: that entity's orbId (used as the path:[] target's orbId)
//   - namespace, name: passed through; used to derive owned-child orbIds via
//     the standard convention `<namespace>:<name>-<suffix>`
//
// Returns one EditTarget for the root + one for each owned child the page
// renders. The set of "rendered" children is determined by the registry's
// owner graph traversal (children whose OwnerType matches `rootType` or any
// of its direct wrappers).
//
// Two-level hierarchies (cluster → ClusterBackup → EtcdBackup/etc.) work
// because BuildEditTargets walks ChildField paths through wrapper types:
// EtcdBackup's path becomes ["backup", "etcd"]. Wrapper types themselves
// (ClusterBackup) are NOT direct edit targets — they're surfaced via
// ParentWrapper on each leaf child so the JS module can emit a wrapper-link
// mutation on first-time configure.
func BuildEditTargets(rootType, rootOrbID, namespace, name string) []EditTarget {
	rootT, ok := nameSet[rootType]
	if !ok {
		return nil
	}

	out := []EditTarget{{
		Path:             []string{},
		Kind:             rootT.Name,
		OrbID:            rootOrbID,
		Fields:           rootT.FormFields,
		JSONStringFields: rootT.JSONStringFields,
		PayloadField:     rootT.PayloadField,
		Namespace:        namespace,
	}}

	// Walk the owner graph one level down. Some children are themselves
	// wrapper types whose grandchildren are the actual edit targets — recurse
	// in that case (wrapper signaled by IsWrapper).
	for _, child := range Children(rootType) {
		if isWrapper(child) {
			// Wrapper: surface its grandchildren as leaves, prefix the path
			// with the wrapper's ChildField on the root.
			wrapperOrbID := fmt.Sprintf("%s:%s-%s", namespace, name, wrapperSuffix(child.Name))
			wrapper := &EditWrapper{
				Kind:        child.Name,
				OrbID:       wrapperOrbID,
				Name:        name + "-" + wrapperSuffix(child.Name),
				Namespace:   namespace,
				ParentField: child.ChildField, // field on the root that points at the wrapper
			}
			for _, leaf := range Children(child.Name) {
				out = append(out, EditTarget{
					Path:               []string{child.ChildField, leaf.ChildField},
					Kind:               leaf.Name,
					OrbID:              fmt.Sprintf("%s:%s-%s", namespace, name, leafSuffix(leaf.Name)),
					Fields:             leaf.FormFields,
					JSONStringFields:   leaf.JSONStringFields,
					PayloadField:       leaf.PayloadField,
					Namespace:          namespace,
					ParentInverseField: leaf.OwnerField,
					ParentOrbID:        wrapperOrbID,
					ParentWrapper:      wrapper,
				})
			}
			continue
		}
		// Direct child (e.g. IdracSettings on Server): one level, no wrapper.
		// Skip children with no editable fields — they're owned but
		// non-editable (KubernetesNode is data-imported via seed/kubectl, not
		// user-edited from the cluster page).
		if len(child.FormFields) == 0 {
			continue
		}
		// Owned-child orbIds follow the deterministic convention
		// `<namespace>:<name>-<suffix>` so first-time-create can derive them.
		out = append(out, EditTarget{
			Path:               []string{child.ChildField},
			Kind:               child.Name,
			OrbID:              fmt.Sprintf("%s:%s-%s", namespace, name, leafSuffix(child.Name)),
			Fields:             child.FormFields,
			JSONStringFields:   child.JSONStringFields,
			PayloadField:       child.PayloadField,
			Namespace:          namespace,
			ParentInverseField: child.OwnerField,
			ParentOrbID:        rootOrbID,
		})
	}
	return out
}

// OverrideEditTargetOrbID lets handlers replace a derived orbId on a target —
// needed when the actual orbId in DGraph doesn't follow the
// `<namespace>:<name>-<suffix>` convention (legacy data, custom IDs).
// Returns a new slice; doesn't mutate the input.
func OverrideEditTargetOrbID(targets []EditTarget, kind, orbID string) []EditTarget {
	out := make([]EditTarget, len(targets))
	for i, t := range targets {
		out[i] = t
		if t.Kind == kind && orbID != "" {
			out[i].OrbID = orbID
		}
	}
	return out
}

// isWrapper returns true for structural-only parent types — types that have
// children but whose own scalar/Boolean fields aren't user-editable. Today
// only ClusterBackup qualifies: it wraps etcd/velero/s3Sync but has no
// editable fields of its own. Wrappers don't appear as edit targets; they
// surface via EditWrapper attached to their children's targets.
func isWrapper(t Type) bool {
	return len(t.FormFields) == 0 && len(Children(t.Name)) > 0
}

// wrapperSuffix returns the URL slug used in derived wrapper orbIds.
// e.g. ClusterBackup → "backup" so the wrapper orbId is `<ns>:<name>-backup`.
// Convention is established by the existing seed data — keep aligned.
func wrapperSuffix(kind string) string {
	switch kind {
	case "ClusterBackup":
		return "backup"
	}
	return strings.ToLower(kind)
}

// leafSuffix returns the URL slug used in derived owned-child orbIds.
// Convention is established by the existing seed data — keep aligned.
func leafSuffix(kind string) string {
	switch kind {
	case "IdracSettings":
		return "idrac"
	case "ServerConfigurationProfile":
		return "scp"
	case "EtcdBackup":
		return "etcd-backup"
	case "VeleroBackup":
		return "velero-backup"
	case "S3Sync":
		return "s3sync"
	}
	return strings.ToLower(kind)
}
