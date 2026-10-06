package configitems

import (
	"fmt"
	"strings"
)

// Edit targets: the list `configitem-editor.js` consumes, one entry per entity
// in the JSON tree a page's edit modal shows.
//
// The structure comes from the page's VIEW — its editable members and theirs —
// while identity comes from the SCHEMA. That split is the whole point: a child's
// orbId must not move because someone re-laid-out a page.

// EditTarget mirrors the shape consumed by web/shared/static/configitem-editor.js.
// Marshaled into the page as a JSON blob so the generic JS module can snapshot,
// diff, and dispatch update{Kind} mutations without page-specific JS.
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
	// so the editor can send it as `version` and have a concurrent edit refused
	// instead of silently overwritten.
	//
	// NOT derivable from a view — BuildEditTargets sees types and conventions,
	// never entity data — so the page handler stamps it from the rows it already
	// fetched (see StampEditTargetVersion). Zero means "not known", and the
	// editor then omits `version` rather than asserting version 0, which would
	// refuse every edit.
	//
	// It travels on the TARGET, deliberately, not in the edit-data tree: that
	// tree is what the JSON editor renders for a human, and the OCC counter is
	// not a field anyone should see or type into.
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
	ParentField string `json:"parentField"` // field on the ROOT type that points at this wrapper
}

// FieldsFor returns a type's editable scalar fields, resolved from the views
// config against the deployed schema. Supplied by the caller rather than read
// here so there is one cache for the whole process.
type FieldsFor func(typeName string) []string

// MetaFor returns a type's schema-derived metadata: its JSON-in-a-String
// fields, its orbId suffix, and the edge its identity derives from.
type MetaFor func(typeName string) TypeInfo

// BuildEditTargets composes the targets list for a root entity's edit modal:
// one for the root, one for each member its page writes.
//
// Structure from the VIEW, identity from the SCHEMA. An owned child may not
// exist yet, so its orbId is DERIVED — `<namespace>:<parentName>-<suffix>` —
// which is what lets the editor address a child the first time someone
// configures it. The caller overrides that with the stored id wherever the fetch
// found one, because a derived id that differs would upsert a phantom entity
// instead of editing the real one.
//
// Two-level hierarchies (cluster → ClusterBackup → EtcdBackup/…) work because
// the walk descends through WRAPPERS: a type with members and no editable fields
// of its own is not a target, it is a path segment, surfaced via ParentWrapper
// so the JS module can create it on a first-time configure.
//
// The page gates only the TOP level (EditorMembers); below it the walk follows
// CONTAINMENT (DependentSingles). A wrapper has no page to declare anything on,
// so reading the page at every level emptied the cluster editor: the data tree
// carried backup.etcd and the target list did not, and the write went nowhere.
func BuildEditTargets(views ViewSet, fields FieldsFor, meta MetaFor, rootType, rootOrbID, namespace, name string, rootValues map[string]string) []EditTarget {
	root := views.Of(rootType)
	if root.Type == "" {
		return nil
	}
	childOrbID := func(childType string) string {
		return derivedChildOrbID(meta, childType, namespace, name, rootValues)
	}

	out := []EditTarget{{
		Path:             []string{},
		Kind:             rootType,
		OrbID:            rootOrbID,
		Fields:           fields(rootType),
		JSONStringFields: JSONStringFieldsFor(meta(rootType)),
		PayloadField:     meta(rootType).PayloadField,
		Namespace:        namespace,
	}}

	for _, child := range root.EditorMembers() {
		// EditorMembers has already excluded lists: a wrapper descent names ONE
		// entity by path — ["backup", "etcd"] — and a path cannot say which row
		// of a list it means.
		if isWrapper(fields, views, child.ChildType) {
			wrapperOrbID := childOrbID(child.ChildType)
			wrapper := &EditWrapper{
				Kind:        child.ChildType,
				OrbID:       wrapperOrbID,
				Name:        name + "-" + orbIDSuffix(meta, child.ChildType),
				Namespace:   namespace,
				ParentField: child.ChildField, // field on the root that points at the wrapper
			}
			for _, leaf := range views.Of(child.ChildType).DependentSingles() {
				out = append(out, EditTarget{
					Path:               []string{child.ChildField, leaf.ChildField},
					Kind:               leaf.ChildType,
					OrbID:              childOrbID(leaf.ChildType),
					Fields:             fields(leaf.ChildType),
					JSONStringFields:   JSONStringFieldsFor(meta(leaf.ChildType)),
					PayloadField:       meta(leaf.ChildType).PayloadField,
					Namespace:          namespace,
					ParentInverseField: leaf.ParentEdge,
					ParentOrbID:        wrapperOrbID,
					ParentWrapper:      wrapper,
				})
			}
			continue
		}
		// A member with no editable fields is owned but not user-editable — a
		// scanned NetworkAdapter whose every field is a hardware fact. It stays
		// in the tree the page fetches; it is not something to offer a human.
		if len(fields(child.ChildType)) == 0 {
			continue
		}
		out = append(out, EditTarget{
			Path:               []string{child.ChildField},
			Kind:               child.ChildType,
			OrbID:              childOrbID(child.ChildType),
			Fields:             fields(child.ChildType),
			JSONStringFields:   JSONStringFieldsFor(meta(child.ChildType)),
			PayloadField:       meta(child.ChildType).PayloadField,
			Namespace:          namespace,
			ParentInverseField: child.ParentEdge,
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
// entities with independent counters, and several targets can share a kind (two
// StorageDevices under one controller). OverrideEditTargetOrbID keys by kind
// because an orbId convention is per-type; this is the opposite question.
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

// isWrapper returns true for structural-only member types — types that own
// members but whose own fields aren't user-editable. ClusterBackup is the
// worked example: it wraps etcd/velero/s3Sync and has no editable fields of its
// own. Wrappers don't appear as edit targets; they surface via EditWrapper on
// their children's targets, so the editor can create one on a first-time
// configure.
func isWrapper(fields FieldsFor, views ViewSet, typeName string) bool {
	return len(fields(typeName)) == 0 && len(views.Of(typeName).DependentSingles()) > 0
}

// orbIDSuffix returns the token an owned child's derived orbId ends with.
//
// This was two switch statements naming six irregular types (IdracSettings is
// "idrac", not "idracsettings"). The convention travels with the type, as an
// `"""orbIdSuffix: ..."""` annotation, and everything else falls back to the
// lower-cased type name. A wrong value here does not error — it builds an orbId
// for an entity that does not exist, and upserts a phantom instead of editing
// the real one.
// derivedChildOrbID builds the orbId an owned child WILL have, from the child
// type's `orbIdPattern:` and the values of the root entity it hangs off.
//
// A owned child's pattern always walks back to the root it is owned in
// — IdracSettings is `{server.serviceTag}-{kind}`, EtcdBackup is
// `{clusterBackupEtcd.cluster.name}-{kind}` — so a path that crosses at least
// one edge is resolved by taking its LAST segment from the root's own values.
// The intermediate hops are the way back to the root and carry no value of
// their own. A single-segment path names a field on the child itself, which
// does not exist yet, so it cannot resolve and must not be guessed at.
//
// This replaced `<namespace>:<rootName>-<suffix>`, which ignored the pattern
// and used the root's NAME. On a Server that is the hostname, so a first-time
// configure minted `<ns>:r04-u25.2f-uae-idrac` while all 155 stored rows read
// `<ns>:<serviceTag>-idrac`. Nothing errored — the next scan simply computed
// the conventional id, failed to find it, and created a SECOND IdracSettings,
// orphaning the first along with its audit history. Verified unbitten on the
// dev graph 2026-10-06 (205/205 children matched the declared patterns), which
// is only because every one of them was seeded rather than UI-created.
//
// Falls back to the old formula when no pattern resolves, which is the deployed
// schema not having been re-applied since the annotations landed. Today's
// behaviour is the right thing to degrade to; the build-time guard is what makes
// sure a MISSING pattern never ships.
func derivedChildOrbID(meta MetaFor, childType, namespace, rootName string, rootValues map[string]string) string {
	legacy := fmt.Sprintf("%s:%s-%s", namespace, rootName, orbIDSuffix(meta, childType))
	if meta == nil {
		return legacy
	}
	info := meta(childType)
	if len(info.OrbIDPattern) == 0 {
		return legacy
	}
	id, err := ConstructOrbID(namespace, OrbIDKindFor(childType, info.Doc), info.OrbIDPattern,
		func(path string) (string, bool) {
			seg := strings.Split(path, ".")
			if len(seg) < 2 {
				return "", false
			}
			v, ok := rootValues[seg[len(seg)-1]]
			v = strings.TrimSpace(v)
			return v, ok && v != ""
		})
	if err != nil {
		return legacy
	}
	return id
}

func orbIDSuffix(meta MetaFor, typeName string) string {
	if meta != nil {
		if s := meta(typeName).OrbIDSuffix; s != "" {
			return s
		}
	}
	return strings.ToLower(typeName)
}
