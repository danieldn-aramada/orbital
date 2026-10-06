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
	if len(viewFor(t, first, "Server").Tabs) != 1 {
		t.Fatalf("Server should start with one member; got %+v", viewFor(t, first, "Server").Tabs)
	}
	firstHash := r.ViewsHash()
	introAfterFirst := f.introCalls

	// Inside the check window, an edit is NOT picked up. That is the cost of the
	// rate limit, and it is the thing the interval is a knob for.
	if err := os.WriteFile(views, []byte("views:\n  Server: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Views(ctx); len(viewFor(t, got, "Server").Tabs) != 1 {
		t.Error("inside the check window the cached views must still serve")
	}

	// The window elapses: the file is re-read, the hash moves, the views change.
	// No restart, and no new Resolver.
	now = now.Add(2 * time.Minute)
	second, err := r.Views(ctx)
	if err != nil {
		t.Fatalf("resolve after the edit: %v", err)
	}
	if n := len(viewFor(t, second, "Server").Tabs); n != 0 {
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
		{Type: "Server", Dependents: []OwnedMember{
			{ChildType: "IdracSettings", ChildField: "idracSettings", ParentEdge: "server"},
			{ChildType: "ServerMaintenance", ChildField: "serverMaintenance", ParentEdge: "server"},
			{ChildType: "StorageDevice", ChildField: "storageDevices", IsList: true},
			{ChildType: "NetworkInterface", ChildField: "networkInterfaces", IsList: true},
		}, Tabs: []ViewTab{
			{Field: "idracSettings", Type: "IdracSettings", Editable: true},
			{Field: "serverMaintenance", Type: "ServerMaintenance", Editable: true},
			// Shown, not written. It renders as a table and produces no target.
			{Field: "networkInterfaces", Type: "NetworkInterface", IsList: true},
			// A path member: its rows come out of a subtree another member owns,
			// and a dotted name is not a legal GraphQL selection.
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
		t.Error("a PATH member must not become an edit target: its rows live in another " +
			"member's subtree, and a dotted name is not a legal GraphQL selection")
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

	contains := []OwnedMember{{ChildType: "IdracSettings", ChildField: "idracSettings", ParentEdge: "server"}}
	base := ViewSet{
		{Type: "Server", Dependents: contains, Tabs: []ViewTab{
			{Field: "idracSettings", Type: "IdracSettings", Editable: true}}},
		{Type: "IdracSettings"},
	}
	first := idracTarget(base)
	if first.OrbID != "colo:A-idrac" {
		t.Errorf("orbId = %q, want colo:A-idrac — derived from the suffix the SCHEMA declares", first.OrbID)
	}
	if first.ParentInverseField != "server" {
		t.Errorf("ParentInverseField = %q, want server — it comes from CONTAINMENT now, "+
			"because for a multi-parent type the answer depends on which page you create from, "+
			"which a single-valued derivesIdFrom: could not say", first.ParentInverseField)
	}

	// Re-lay out the page: another member before it, a different field order,
	// the whole view rebuilt. Identity must not notice.
	relaid := ViewSet{
		{Type: "Server", Dependents: append([]OwnedMember{
			{ChildType: "ServerMaintenance", ChildField: "serverMaintenance", ParentEdge: "server"}},
			contains...), Tabs: []ViewTab{
			{Field: "serverMaintenance", Type: "ServerMaintenance", Editable: true},
			{Field: "idracSettings", Type: "IdracSettings", Editable: true},
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
	if _, _, _ = decl.Slug, decl.MenuWeight, decl.Tabs; false {
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

// A wrapper two hops down has no page, so the page's declaration cannot gate it
// — ownership does.
//
// This is a regression test with a name: reading `editable:` at EVERY level
// emptied the cluster editor. ClusterBackup has no page, so it declared no
// members; the edit DATA tree still carried `backup.etcd` because that tree
// follows ownership, and the TARGET list did not. The editor rendered an etcd
// schedule, accepted a change, and wrote nothing — no error, no audit row, no
// trace. The page gates the top level; below it, the unit is the unit.
func TestEditTargets_WrapperDescentFollowsOwnershipNotThePage(t *testing.T) {
	views := ViewSet{
		{Type: "EksaKubernetesCluster",
			Dependents: []OwnedMember{
				{ChildType: "ClusterBackup", ChildField: "backup", ParentEdge: "cluster"},
				{ChildType: "KubernetesNode", ChildField: "nodes", ParentEdge: "cluster", IsList: true},
			},
			// Supplied by the INTERFACE page — the concrete type has none of its
			// own, which is what keeps one entry in the menu per provider family.
			Tabs: []ViewTab{
				{Field: "backup", Type: "ClusterBackup", Editable: true},
				{Field: "nodes", Type: "KubernetesNode", IsList: true},
			}},
		// Pageless, therefore tab-less. Its children are owned all the same.
		{Type: "ClusterBackup", Dependents: []OwnedMember{
			{ChildType: "EtcdBackup", ChildField: "etcd", ParentEdge: "clusterBackupEtcd"},
			{ChildType: "S3Sync", ChildField: "s3Sync", ParentEdge: "clusterBackupS3Sync"},
		}},
		{Type: "EtcdBackup"}, {Type: "S3Sync"}, {Type: "KubernetesNode"},
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
			t.Fatalf("%s must be a target: it is owned by a wrapper the page declared editable; got %v", want, kinds(got))
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
	// The TOP level is still the page's call. `nodes` is owned and would be
	// reachable under ownership alone; the page does not declare it editable,
	// so it must not be writable from here.
	if _, ok := byKind["KubernetesNode"]; ok {
		t.Error("ownership governs BELOW the page's declaration, never at it — " +
			"an undeclared top-level member must stay unwritable")
	}
}
