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
// Structure from the VIEW, identity from the SCHEMA. A subgraph member may not
// exist yet, so its orbId is DERIVED — `<namespace>:<parentName>-<suffix>` —
// which is what lets the editor address a child the first time someone
// configures it. The caller overrides that with the stored id wherever the fetch
// found one, because a derived id that differs would upsert a phantom entity
// instead of editing the real one.
//
// Every target is a subgraph member the page declares `editable: true`. A
// two-hop member (cluster → backup → etcd) carries its intermediate as a
// ParentWrapper, so the JS module can create that intermediate on a first-time
// configure — ClusterBackup has no editable fields and exists only to hold its
// three sub-kinds.
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

	for _, m := range root.EditorMembers() {
		// A member with no editable fields is not something to offer a human —
		// a scanned NetworkAdapter whose every field is a hardware fact, or a
		// ClusterBackup that only holds its sub-kinds.
		if len(fields(m.Type)) == 0 {
			continue
		}
		t := EditTarget{
			Path:               strings.Split(m.Field, "."),
			Kind:               m.Type,
			OrbID:              childOrbID(m.Type),
			Fields:             fields(m.Type),
			JSONStringFields:   JSONStringFieldsFor(meta(m.Type)),
			PayloadField:       meta(m.Type).PayloadField,
			Namespace:          namespace,
			ParentInverseField: m.ParentEdge,
			ParentOrbID:        rootOrbID,
		}
		if len(t.Path) == 2 {
			// Validate refuses `editable` beyond two hops, so this is the only
			// intermediate there can be.
			mid := root.Relations[t.Path[0]]
			t.ParentOrbID = childOrbID(mid)
			t.ParentWrapper = &EditWrapper{
				Kind:        mid,
				OrbID:       t.ParentOrbID,
				Name:        name + "-" + orbIDSuffix(meta, mid),
				Namespace:   namespace,
				ParentField: t.Path[0],
			}
		}
		out = append(out, t)
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
// Keyed by orbId rather than kind: a root and its subgraph members are different
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

// orbIDSuffix returns the token a subgraph member's derived orbId ends with.
//
// This was two switch statements naming six irregular types (IdracSettings is
// "idrac", not "idracsettings"). The convention travels with the type, as an
// `"""orbIdSuffix: ..."""` annotation, and everything else falls back to the
// lower-cased type name. A wrong value here does not error — it builds an orbId
// for an entity that does not exist, and upserts a phantom instead of editing
// the real one.
// derivedChildOrbID builds the orbId a subgraph member WILL have, from the child
// type's `orbIdPattern:` and the values of the root entity it hangs off.
//
// A subgraph member's pattern always walks back to the root it hangs off
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
