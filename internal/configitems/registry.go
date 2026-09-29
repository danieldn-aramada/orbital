// Package configitems is orbital's single source of truth for the set of
// ConfigItem types declared in schema/schema.graphql.
//
// # Background — why this exists
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
//  1. Declare it in schema/schema.graphql
//  2. Add a Type entry below
//
// That's it. The Go audit pipeline, before-fetcher, and (via the
// configitem-editor JS module) the front-end editor all pick it up.
//
// # Boundaries
//
// This registry describes only ConfigItem-shaped types — entities with an
// orbId and a place in the parent/child relationship graph. It does NOT
// describe arbitrary GraphQL types, scalar enums, or query-only interfaces.
// The per-type EDITABLE FIELD lists that used to live here are gone: they are
// derived from the deployed schema at runtime (see derive.go). What remains is
// the containment/ownership policy, which introspection cannot supply because
// `@hasInverse` is declared on both ends of an edge and says nothing about
// which end is the parent.
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

	// OwnerEdges is the ownership type-policy for the diff-preview rollup
	// (Spike 30, upward walk) and the audit collector (downward walk), for types
	// whose ownership is NOT a single primary owner. It is an ordered list of
	// candidate owners, most-specific first: the FIRST edge whose Field is
	// present on a given node instance is that node's canonical presentation
	// parent (mirrors Kubernetes' single controller:true among many
	// ownerReferences). Precedence is list order — never a Go map's randomized
	// iteration.
	//
	// Leave empty for single-owner types and roots: single-owner ownership is
	// derived from {OwnerType, OwnerField, ChildField} above (see OwnerEdgesOf).
	// Declare explicitly only for multi-parent types (NetworkInterface,
	// IPAddress) or where the up-owner differs from the down-path (StorageVolume,
	// whose required storageController edge is its rollup parent even though it
	// is reached downward via StorageDevice.storageVolumes).
	OwnerEdges []OwnerEdge
}

// OwnerEdge is one candidate owner of a ConfigItem type — the type-policy the
// diff-preview rollup and the audit collector both derive from, so they can
// never drift. It captures BOTH directions of the same @hasInverse edge:
//   - Field:     the field on the CHILD pointing up to the owner (e.g. IPAddress
//     has Field "serverOobIP"). Used by graphdiff's upward owner-of walk.
//   - DownField: the field on the OWNER pointing down at the child (e.g. Server
//     has DownField "oobIP"). Used by the audit collector's downward walk.
//     Empty when no direct down field exists (e.g. StorageVolume→StorageController
//     has no StorageController.storageVolumes; that child is reached downward via
//     its single-owner ChildField instead).
type OwnerEdge struct {
	OwnerType string
	Field     string
	DownField string
}

// OwnerEdgesOf returns the ordered ownership edges for a type — the explicit
// Type.OwnerEdges when declared, otherwise the single-owner edge derived from
// {OwnerType, OwnerField, ChildField}. Roots and un-owned types return nil.
// This is the ONE place both the diff rollup and the audit collector read
// ownership from.
func (t Type) OwnerEdgesOf() []OwnerEdge {
	if len(t.OwnerEdges) > 0 {
		return t.OwnerEdges
	}
	if t.OwnerType != "" && t.OwnerField != "" {
		return []OwnerEdge{{OwnerType: t.OwnerType, Field: t.OwnerField, DownField: t.ChildField}}
	}
	return nil
}

// OwnerEdgePredicates returns the ordered upward owner-edge predicates
// ("<ChildType>.<field>", most-specific first within a type) — the single
// source the diff-preview rollup (graphdiff.ownerEdges) derives from. Order
// across types is irrelevant (a node carries only one type's predicates); order
// WITHIN a type is the precedence that selects the canonical parent.
func OwnerEdgePredicates() []string {
	var out []string
	for _, t := range Types {
		for _, e := range t.OwnerEdgesOf() {
			out = append(out, t.Name+"."+e.Field)
		}
	}
	return out
}

// OwnedChild is a downward ownership link for the audit collector: the owner
// owns ChildType, reachable via ChildField (the owner-side @hasInverse pointer).
type OwnedChild struct {
	ChildType  string
	ChildField string
}

// downwardEdges returns every owner edge of t that has a usable DOWN field — the
// explicit OwnerEdges plus the single-owner {OwnerType, ChildField}. Used for
// the downward audit walk (the upward rollup uses OwnerEdgesOf instead).
func downwardEdges(t Type) []OwnerEdge {
	edges := append([]OwnerEdge(nil), t.OwnerEdges...)
	if t.OwnerType != "" && t.ChildField != "" {
		edges = append(edges, OwnerEdge{OwnerType: t.OwnerType, Field: t.OwnerField, DownField: t.ChildField})
	}
	return edges
}

// OwnedChildren returns every ConfigItem type owned by parentType, downward —
// the single source the audit collector derives from. Honors interface
// ownership (a concrete type gets children that name an interface it
// implements, e.g. EksaKubernetesCluster gets KubernetesNode/ClusterBackup).
// Deduplicated per (ChildType, ChildField).
func OwnedChildren(parentType string) []OwnedChild {
	return OwnedChildrenWith(parentType, nil)
}

// OwnedChildrenWith is OwnedChildren with an explicit interface lookup.
//
// It exists to break a RE-ENTRANCY loop. The package-level lookup reads through
// the schema resolver, and the resolver builds its view list inside its own
// resolve step — so anything calling OwnedChildren from there re-entered the
// resolver, which re-resolved, which called OwnedChildren again. Every generic
// page hung, with no error and nothing in the access log, because the request
// never finished.
//
// A caller that already HOLDS the snapshot passes its own lookup and never
// re-enters. Pass nil to use the package-level one.
func OwnedChildrenWith(parentType string, implements func(string) []string) []OwnedChild {
	parentT, parentKnown := nameSet[parentType]
	implementsFn := func(typeName, iface string) bool {
		if implements == nil {
			return implementsInterface(typeName, iface)
		}
		for _, i := range implements(typeName) {
			if i == iface {
				return true
			}
		}
		return false
	}
	owns := func(ownerType string) bool {
		return ownerType == parentType ||
			(parentKnown && implementsFn(parentT.Name, ownerType))
	}
	seen := make(map[OwnedChild]bool)
	var out []OwnedChild
	for _, t := range Types {
		for _, e := range downwardEdges(t) {
			if e.DownField == "" || !owns(e.OwnerType) {
				continue
			}
			oc := OwnedChild{ChildType: t.Name, ChildField: e.DownField}
			if !seen[oc] {
				seen[oc] = true
				out = append(out, oc)
			}
		}
	}
	return out
}

// OwnedOrbIDSelection returns a GraphQL sub-selection fetching every owned
// descendant's orbId under rootType, built from the downward ownership graph —
// the single source the audit collector's related-orbId query derives from.
// Deterministic (Types slice order). Path/depth-guarded against a
// (schema-impossible) ownership type cycle.
func OwnedOrbIDSelection(rootType string) string {
	var b strings.Builder
	var rec func(typeName string, path map[string]bool, depth int)
	rec = func(typeName string, path map[string]bool, depth int) {
		if depth > 8 || path[typeName] {
			return
		}
		path[typeName] = true
		for _, c := range OwnedChildren(typeName) {
			b.WriteString(c.ChildField)
			b.WriteString(" { orbId ")
			rec(c.ChildType, path, depth+1)
			b.WriteString("} ")
		}
		delete(path, typeName)
	}
	rec(rootType, map[string]bool{}, 0)
	return strings.TrimSpace(b.String())
}

// Types is THE registry. Adding a new ConfigItem starts here.
//
// Ordering convention: parents before children; types within a family grouped
// together. Order doesn't matter for derived values but keeps the file
// readable when grepping.
var Types = []Type{
	// ── Inventory hierarchy ──────────────────────────────────────────────────
	{
		Name: "DataCenter",
	},
	{
		Name:       "Rack",
		OwnerType:  "DataCenter",
		ChildField: "racks",
	},
	{
		Name: "Server",
	},
	{
		Name:       "IdracSettings",
		OwnerType:  "Server",
		OwnerField: "server",
		ChildField: "idracSettings",
	},
	{
		Name:       "ServerMaintenance",
		OwnerType:  "Server",
		OwnerField: "server",
		ChildField: "serverMaintenance",
	},
	{
		Name:       "ServerConfigurationProfile",
		OwnerType:  "Server",
		OwnerField: "server",
		ChildField: "serverConfigurationProfile",
	},
	{
		Name:       "StorageController",
		OwnerType:  "Server",
		OwnerField: "server",
		ChildField: "storageControllers",
	},
	{
		Name:       "StorageDevice",
		OwnerType:  "StorageController",
		OwnerField: "storageController",
		ChildField: "storageDevices",
	},
	{
		Name:       "StorageVolume",
		OwnerType:  "StorageDevice",
		ChildField: "storageVolumes",
		OwnerEdges: []OwnerEdge{
			{OwnerType: "StorageController", Field: "storageController"},
		},
	},

	// ── IP addresses (referenced by many types via @hasInverse) ──────────────
	{
		Name: "IPAddress",
		OwnerEdges: []OwnerEdge{
			{OwnerType: "Server", Field: "serverOobIP", DownField: "oobIP"},
			{OwnerType: "KubernetesNode", Field: "kubernetesNodeIpv4", DownField: "ipv4"},
			{OwnerType: "KubernetesCluster", Field: "kubernetesClusterControlPlaneEndpoint", DownField: "controlPlaneEndpoint"},
			{OwnerType: "EksaKubernetesCluster", Field: "eksaKubernetesClusterTinkerbellIP", DownField: "tinkerbellIP"},
		},
	},

	// ── Network devices (switch / router / firewall / sd-wan) ────────────────
	{
		Name: "NetworkDevice",
	},

	// ── Server NICs (read-only, like storage — owned children for cascade) ───
	{
		Name:       "NetworkAdapter",
		OwnerType:  "Server",
		OwnerField: "server",
		ChildField: "networkAdapters",
	},
	{
		Name:       "NetworkInterface",
		OwnerType:  "NetworkAdapter",
		OwnerField: "networkAdapter",
		ChildField: "networkInterfaces",
		OwnerEdges: []OwnerEdge{
			{OwnerType: "NetworkAdapter", Field: "networkAdapter", DownField: "networkInterfaces"},
			{OwnerType: "Server", Field: "server", DownField: "networkInterfaces"},
			{OwnerType: "NetworkDevice", Field: "networkDevice", DownField: "networkInterfaces"},
		},
	},

	// ── Kubernetes cluster hierarchy ─────────────────────────────────────────
	{
		// KubernetesCluster is an INTERFACE, and this entry exists for the audit
		// allowlist — NOT for mutations.
		//
		// Corrected 2026-09-03: it used to claim it covered interface-level
		// update/delete. It cannot. `KubernetesClusterFilter` has no `orbId` (an
		// interface filter carries only its own @search fields, and orbId is
		// declared on ConfigItem), so orbital's `filter: { orbId: { eq: $orbId } }`
		// form cannot target it — verified by introspection. Writes go through the
		// concrete types, which do carry orbId and version.
		Name: "KubernetesCluster",
	},
	{
		Name: "EksaKubernetesCluster",
	},
	{
		Name:       "KubernetesNode",
		OwnerType:  "KubernetesCluster",
		OwnerField: "cluster",
		ChildField: "nodes",
	},
	{
		Name:       "ClusterBackup",
		OwnerType:  "KubernetesCluster",
		OwnerField: "cluster",
		ChildField: "backup",
	},
	{
		Name:       "EtcdBackup",
		OwnerType:  "ClusterBackup",
		OwnerField: "clusterBackupEtcd",
		ChildField: "etcd",
	},
	{
		Name:       "VeleroBackup",
		OwnerType:  "ClusterBackup",
		OwnerField: "clusterBackupVelero",
		ChildField: "velero",
	},
	{
		Name:       "S3Sync",
		OwnerType:  "ClusterBackup",
		OwnerField: "clusterBackupS3Sync",
		ChildField: "s3Sync",
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
// Names returns every registered ConfigItem type name, sorted. Used where a
// caller needs to offer or validate the set of types (approval-policy
// selectors), so the list can never drift from the registry.
func Names() []string {
	out := make([]string, 0, len(Types))
	for _, t := range Types {
		out = append(out, t.Name)
	}
	slices.Sort(out)
	return out
}

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
		if parentKnown && t.OwnerType != "" && implementsInterface(parentT.Name, t.OwnerType) {
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

	// Version is the entity's OCC counter at the moment the page was rendered,
	// so the editor can send it as `version` and have a concurrent edit
	// refused instead of silently overwritten.
	//
	// NOT derivable from the registry — BuildEditTargets sees only types and
	// naming conventions, never entity data — so the page handler stamps it
	// from the rows it already fetched (see StampEditTargetVersion). Zero means
	// "not known", and the editor then omits `version` rather than asserting
	// version 0, which would refuse every edit.
	//
	// It travels on the TARGET, deliberately, not in the edit-data tree: that
	// tree is what the JSON editor renders for a human, and the OCC counter is
	// not a field anyone should see or type into. `withoutStamped` exists to
	// keep it out of `set`, and that stays true.
	Version int `json:"version,omitempty"`
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
// FieldsFor returns a type's editable scalar fields. It is supplied by the
// caller rather than read from this file so the field list can come from the
// DEPLOYED schema — see internal/configitems/derive.go. A type with no editable
// fields returns empty, which is how a child is excluded from the edit tree.
type FieldsFor func(typeName string) []string

// MetaFor returns a type's schema-derived metadata: its JSON-in-a-String
// fields and its orbId suffix. Supplied by the caller for the same reason
// FieldsFor is — both used to be hand-maintained in this file.
type MetaFor func(typeName string) TypeInfo

func BuildEditTargets(fields FieldsFor, meta MetaFor, rootType, rootOrbID, namespace, name string) []EditTarget {
	rootT, ok := nameSet[rootType]
	if !ok {
		return nil
	}

	out := []EditTarget{{
		Path:             []string{},
		Kind:             rootT.Name,
		OrbID:            rootOrbID,
		Fields:           fields(rootT.Name),
		JSONStringFields: JSONStringFieldsFor(meta(rootT.Name)),
		PayloadField:     meta(rootT.Name).PayloadField,
		Namespace:        namespace,
	}}

	// Walk the owner graph one level down. Some children are themselves
	// wrapper types whose grandchildren are the actual edit targets — recurse
	// in that case (wrapper signaled by IsWrapper).
	for _, child := range Children(rootType) {
		if isWrapper(fields, child) {
			// Wrapper: surface its grandchildren as leaves, prefix the path
			// with the wrapper's ChildField on the root.
			wrapperOrbID := fmt.Sprintf("%s:%s-%s", namespace, name, orbIDSuffix(meta, child.Name))
			wrapper := &EditWrapper{
				Kind:        child.Name,
				OrbID:       wrapperOrbID,
				Name:        name + "-" + orbIDSuffix(meta, child.Name),
				Namespace:   namespace,
				ParentField: child.ChildField, // field on the root that points at the wrapper
			}
			for _, leaf := range Children(child.Name) {
				out = append(out, EditTarget{
					Path:               []string{child.ChildField, leaf.ChildField},
					Kind:               leaf.Name,
					OrbID:              fmt.Sprintf("%s:%s-%s", namespace, name, orbIDSuffix(meta, leaf.Name)),
					Fields:             fields(leaf.Name),
					JSONStringFields:   JSONStringFieldsFor(meta(leaf.Name)),
					PayloadField:       meta(leaf.Name).PayloadField,
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
		if len(fields(child.Name)) == 0 {
			continue
		}
		// Owned-child orbIds follow the deterministic convention
		// `<namespace>:<name>-<suffix>` so first-time-create can derive them.
		out = append(out, EditTarget{
			Path:               []string{child.ChildField},
			Kind:               child.Name,
			OrbID:              fmt.Sprintf("%s:%s-%s", namespace, name, orbIDSuffix(meta, child.Name)),
			Fields:             fields(child.Name),
			JSONStringFields:   JSONStringFieldsFor(meta(child.Name)),
			PayloadField:       meta(child.Name).PayloadField,
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

// StampEditTargetVersion records an entity's current OCC version on its edit
// target so the editor can send `version`.
//
// Keyed by orbId rather than kind: a root and its owned children are different
// entities with independent counters, and several targets can share a kind
// (two StorageDevices under one controller). OverrideEditTargetOrbID keys by
// kind because an orbId convention is per-type; this is the opposite question.
func StampEditTargetVersion(targets []EditTarget, orbID string, version int) []EditTarget {
	if orbID == "" || version <= 0 {
		return targets
	}
	out := make([]EditTarget, len(targets))
	for i, t := range targets {
		out[i] = t
		if t.OrbID == orbID {
			out[i].Version = version
		}
	}
	return out
}

// isWrapper returns true for structural-only parent types — types that have
// children but whose own scalar/Boolean fields aren't user-editable. Today
// only ClusterBackup qualifies: it wraps etcd/velero/s3Sync but has no
// editable fields of its own. Wrappers don't appear as edit targets; they
// surface via EditWrapper attached to their children's targets.
func isWrapper(fields FieldsFor, t Type) bool {
	return len(fields(t.Name)) == 0 && len(Children(t.Name)) > 0
}

// orbIDSuffix returns the token an owned child's derived orbId ends with.
//
// This was two switch statements naming six irregular types (IdracSettings is
// "idrac", not "idracsettings"). The convention now travels with the type, as a
// `"""orbIdSuffix: ..."""` annotation, and everything else falls back to the
// lower-cased type name. A wrong value here does not error — it builds an orbId
// for an entity that does not exist, and upserts a phantom instead of editing
// the real one.
func orbIDSuffix(meta MetaFor, typeName string) string {
	if meta != nil {
		if s := meta(typeName).OrbIDSuffix; s != "" {
			return s
		}
	}
	return strings.ToLower(typeName)
}

// implementsInterface reports whether a concrete type implements an interface.
//
// Used to resolve interface-typed ownership: the backup sub-kinds declare an
// owner of `KubernetesCluster`, and a concrete EksaKubernetesCluster owns them
// because it implements that interface.
//
// Reads the DEPLOYED schema through a package-level hook rather than a
// hand-maintained `Implements` list. The hook is a package var because Children
// and downwardEdges are package functions called from several places that have
// no resolver to thread; when it is unset (unit tests with no schema) the answer
// is false, which is the same as the empty list those tests used to see.
var implementsLookup func(typeName string) []string

// SetImplementsLookup wires the schema-backed interface lookup at startup.
func SetImplementsLookup(fn func(typeName string) []string) { implementsLookup = fn }

// ImplementsFor returns the interfaces a type declares in the deployed schema,
// or nil when no schema has been read.
func ImplementsFor(typeName string) []string {
	if implementsLookup == nil {
		return nil
	}
	return implementsLookup(typeName)
}

func implementsInterface(typeName, iface string) bool {
	if implementsLookup == nil {
		return false
	}
	for _, i := range implementsLookup(typeName) {
		if i == iface {
			return true
		}
	}
	return false
}

// ExclusivelyOwned reports whether exactly one kind of parent can own this
// type.
//
// This is what separates a child that belongs to its parent from one that
// merely hangs off it. ClusterBackup is only ever a cluster's, so a cluster
// page shows it inline and edits it through the parent's JSON tree. IPAddress
// is reachable from a server, a node and two cluster fields — it is a thing in
// its own right, and a page that inlined it would be claiming an ownership
// nobody has.
func ExclusivelyOwned(typeName string) bool {
	t, known := nameSet[typeName]
	if !known {
		return false
	}
	return len(t.OwnerEdgesOf()) == 1
}
