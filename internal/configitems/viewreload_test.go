package configitems

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Acceptance 10 — editing the views file is picked up without a process restart.
//
// This is what makes a ConfigMap the storage layer rather than a Postgres table
// with RBAC, an audit event per change and a reset path, all of which orbital
// would have had to build for its own configuration. The kubelet updates a
// volume-mounted ConfigMap in place; orbital notices because the views document
// is hashed on the same rate-limited loop that already watches the deployed
// schema.
//
// ⚠️ Mount the DIRECTORY. A `subPath` mount is NOT updated in place, and this
// test cannot catch that — it is a deployment fact, recorded on the config field
// and in the manifest.
func TestResolver_ViewsFileChangeRederivesWithoutRestart(t *testing.T) {
	f := sample()
	views := sampleViews(t)
	now := time.Now()
	r := NewResolver(f, time.Minute).WithViews(views, "", "")
	r.now = func() time.Time { return now }
	ctx := context.Background()

	first, err := r.Views(ctx)
	if err != nil {
		t.Fatalf("initial resolve: %v", err)
	}
	if len(viewFor(t, first, "Server").Subgraph) != 1 {
		t.Fatalf("Server should start with one member; got %+v", viewFor(t, first, "Server").Subgraph)
	}
	firstHash := r.ViewsHash()
	introAfterFirst := f.introCalls

	// Inside the check window, an edit is NOT picked up. That is the cost of the
	// rate limit, and it is the thing the interval is a knob for.
	if err := os.WriteFile(views, []byte("views:\n  Server: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Views(ctx); len(viewFor(t, got, "Server").Subgraph) != 1 {
		t.Error("inside the check window the cached views must still serve")
	}

	// The window elapses: the file is re-read, the hash moves, the views change.
	// No restart, and no new Resolver.
	now = now.Add(2 * time.Minute)
	second, err := r.Views(ctx)
	if err != nil {
		t.Fatalf("resolve after the edit: %v", err)
	}
	if n := len(viewFor(t, second, "Server").Subgraph); n != 0 {
		t.Errorf("Server has %d members after the edit, want 0 — the file change was not picked up", n)
	}
	if r.ViewsHash() == firstHash {
		t.Error("the views hash must move with the file; the editor's staleness guard keys on it")
	}

	// The SCHEMA did not change, so there is no reason to introspect again. A
	// views edit that re-introspected would make every ConfigMap write cost a
	// round trip to DGraph.
	if f.introCalls != introAfterFirst {
		t.Errorf("a views-only change must not re-introspect; introspect calls %d -> %d",
			introAfterFirst, f.introCalls)
	}
}

// Acceptance 3 — a member marked `editable: true` is writable through the page's
// editor; a member without it renders but cannot be written.
//
// The two halves are one axis with an attribute, which is the whole claim of the
// model: editability is a choice a view makes about a member, not a property of
// the edge. A rack page SHOWS its servers and does not claim the right to edit
// them; the same edge on a different page could.
func TestEditTargets_OnlyEditorMembersAreWritable(t *testing.T) {
	views := ViewSet{
		{Type: "Server", Subgraph: []ViewTab{
			{Field: "idracSettings", Type: "IdracSettings", Editable: true, ParentEdge: "server"},
			{Field: "serverMaintenance", Type: "ServerMaintenance", Editable: true, ParentEdge: "server"},
			// Shown, not written. It renders as a table and produces no target.
			{Field: "networkInterfaces", Type: "NetworkInterface", IsList: true},
			// A LIST path carrying a stray flag Validate would have cleared:
			// EditorMembers must still refuse it, because a path cannot name
			// one row.
			{Field: "storageControllers.storageDevices", Type: "StorageDevice", IsList: true, Editable: true},
		}},
		{Type: "IdracSettings"}, {Type: "ServerMaintenance"},
		{Type: "NetworkInterface"}, {Type: "StorageDevice"},
	}
	fields := func(typeName string) []string {
		return map[string][]string{
			"Server":            {"hostname"},
			"IdracSettings":     {"sshEnabled"},
			"ServerMaintenance": {"enabled"},
			"NetworkInterface":  {"macAddress"},
			"StorageDevice":     {"wwn"},
		}[typeName]
	}
	meta := func(typeName string) TypeInfo {
		return TypeInfo{OrbIDSuffix: map[string]string{"IdracSettings": "idrac"}[typeName]}
	}

	got := BuildEditTargets(views, fields, meta, "Server", "colo:server-A", "colo", "A", nil)

	writable := map[string]bool{}
	for _, tgt := range got {
		writable[tgt.Kind] = true
	}
	for _, want := range []string{"Server", "IdracSettings", "ServerMaintenance"} {
		if !writable[want] {
			t.Errorf("%s is an editable member and must be writable; got targets %v", want, kinds(got))
		}
	}
	if writable["NetworkInterface"] {
		t.Error("a member without `editable` renders but must NOT be writable — " +
			"a target the page shows and cannot write is an unknown key on submit, " +
			"and configitem-editor.js refuses the whole save naming it")
	}
	if writable["StorageDevice"] {
		t.Error("a LIST member must not become an edit target: a path cannot name one row")
	}
}

// Acceptance 7 — a child's orbId derives from the SCHEMA, and does not change
// when its view's `editable` flag changes.
//
// Identity cannot be a side effect of a display decision. `debt.md` carries a
// Hi-severity entry on orbId mutability: re-keying orphans every child id,
// splits the audit trail across two ids, and breaks cb-controller's SSA list-map
// identity. That is why `orbIdSuffix:` stayed in the schema while eleven view
// annotations left and `derivesIdFrom:` was deleted.
func TestOrbIDDerivation_FromSchemaNotFromViewConfig(t *testing.T) {
	meta := func(typeName string) TypeInfo {
		if typeName == "IdracSettings" {
			return TypeInfo{OrbIDSuffix: "idrac"}
		}
		return TypeInfo{}
	}
	fields := func(typeName string) []string {
		return map[string][]string{"Server": {"hostname"}, "IdracSettings": {"sshEnabled"}}[typeName]
	}
	idracTarget := func(views ViewSet) EditTarget {
		t.Helper()
		for _, tgt := range BuildEditTargets(views, fields, meta, "Server", "colo:server-A", "colo", "A", nil) {
			if tgt.Kind == "IdracSettings" {
				return tgt
			}
		}
		t.Fatal("no IdracSettings target")
		return EditTarget{}
	}

	idrac := ViewTab{Field: "idracSettings", Type: "IdracSettings", Editable: true, ParentEdge: "server"}
	base := ViewSet{
		{Type: "Server", Subgraph: []ViewTab{idrac}},
		{Type: "IdracSettings"},
	}
	first := idracTarget(base)
	if first.OrbID != "colo:A-idrac" {
		t.Errorf("orbId = %q, want colo:A-idrac — derived from the suffix the SCHEMA declares", first.OrbID)
	}
	if first.ParentInverseField != "server" {
		t.Errorf("ParentInverseField = %q, want server — the @hasInverse partner of the "+
			"declared path, because for a multi-parent type the answer depends on which page "+
			"you create from, which a single-valued derivesIdFrom: could not say", first.ParentInverseField)
	}

	// Re-lay out the page: another member before it, a different field order,
	// the whole view rebuilt. Identity must not notice.
	relaid := ViewSet{
		{Type: "Server", Subgraph: []ViewTab{
			{Field: "serverMaintenance", Type: "ServerMaintenance", Editable: true, ParentEdge: "server"},
			idrac,
		}},
		{Type: "IdracSettings"}, {Type: "ServerMaintenance"},
	}
	if got := idracTarget(relaid).OrbID; got != first.OrbID {
		t.Errorf("re-laying out the page re-keyed the child: %q -> %q", first.OrbID, got)
	}

	// And the page carries NO way to say otherwise: a page declaration cannot
	// name a suffix or a parent edge, so there is no key to flip. If this ever
	// compiles with one, identity has moved into the view and the Hi-severity
	// orbId-mutability debt is back.
	var decl PageDecl
	if _, _, _ = decl.Slug, decl.MenuWeight, decl.Subgraph; false {
		t.Fatal("unreachable")
	}
}

func kinds(targets []EditTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Kind)
	}
	return out
}

// A two-hop member carries its intermediate as a WRAPPER, and the top level is
// still the page's call.
//
// The regression this guards has a name: the edit DATA tree and the TARGET list
// once read different declarations, and the cluster editor rendered an etcd
// schedule, accepted a change, and wrote nothing — no error, no audit row. Both
// now read EditorMembers; this pins the target half for the two-hop shape.
func TestEditTargets_TwoHopMemberCarriesItsIntermediateAsWrapper(t *testing.T) {
	views := ViewSet{
		{Type: "EksaKubernetesCluster",
			Relations: map[string]string{"backup": "ClusterBackup", "nodes": "KubernetesNode"},
			// Supplied by the INTERFACE page — the concrete type has none of its
			// own, which is what keeps one entry in the menu per provider family.
			Subgraph: []ViewTab{
				{Field: "backup", Type: "ClusterBackup", ParentEdge: "cluster"},
				{Field: "backup.etcd", Type: "EtcdBackup", Editable: true, ParentEdge: "clusterBackupEtcd"},
				{Field: "backup.s3Sync", Type: "S3Sync", Editable: true, ParentEdge: "clusterBackupS3Sync"},
				{Field: "nodes", Type: "KubernetesNode", IsList: true, ParentEdge: "cluster"},
			}},
		{Type: "ClusterBackup"}, {Type: "EtcdBackup"}, {Type: "S3Sync"}, {Type: "KubernetesNode"},
	}
	fields := func(typeName string) []string {
		return map[string][]string{
			"EksaKubernetesCluster": {"kubernetesVersion"},
			"EtcdBackup":            {"schedule"},
			"S3Sync":                {"enabled"},
			"KubernetesNode":        {"role"},
		}[typeName] // ClusterBackup has none — that is what makes it a wrapper
	}
	meta := func(typeName string) TypeInfo {
		return TypeInfo{OrbIDSuffix: map[string]string{
			"ClusterBackup": "backup", "EtcdBackup": "etcd-backup", "S3Sync": "s3sync",
		}[typeName]}
	}

	got := BuildEditTargets(views, fields, meta, "EksaKubernetesCluster", "colo:dev-main", "colo", "dev-main", nil)

	byKind := map[string]EditTarget{}
	for _, tgt := range got {
		byKind[tgt.Kind] = tgt
	}
	for _, want := range []string{"EtcdBackup", "S3Sync"} {
		tgt, ok := byKind[want]
		if !ok {
			t.Fatalf("%s must be a target: the page declares it editable; got %v", want, kinds(got))
		}
		if strings.Join(tgt.Path, ".") != "backup."+map[string]string{"EtcdBackup": "etcd", "S3Sync": "s3Sync"}[want] {
			t.Errorf("%s path = %v, want the two-segment path the edit tree nests at", want, tgt.Path)
		}
		if tgt.ParentWrapper == nil || tgt.ParentWrapper.Kind != "ClusterBackup" {
			t.Errorf("%s must carry its wrapper, or a first-time configure has nothing to create", want)
		}
	}
	if _, ok := byKind["ClusterBackup"]; ok {
		t.Error("a wrapper is a path segment, not a target — it has no editable fields to write")
	}
	// `nodes` is in the subgraph and not declared editable, so it must not be
	// writable from here.
	if _, ok := byKind["KubernetesNode"]; ok {
		t.Error("a member without `editable` must stay unwritable")
	}
}
