//go:build integration

package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The cascade is derived from the view: an editable member is part of this
// page's unit — edited with it, audited with it, and DELETED with it.
//
// These tests exist because the three hand-written traversals this replaced had
// drifted into four silent defects, and nothing could have caught them: there
// was no model to check the code against. Each test below pins one half of the
// sentence, or one of the defects.

const (
	cascadeDC     = crNS + ":dc-cascade"
	cascadeServer = crNS + ":server-cascade"
	cascadeRack   = crNS + ":rack-cascade"
	cascadeNIC    = crNS + ":nic-cascade"
	cascadeSCP    = crNS + ":scp-cascade"
	cascadeIP     = crNS + ":ip-cascade"
	cascadeNetDev = crNS + ":netdev-cascade"
	cascadeNode   = crNS + ":knode-cascade"
	cascadeClust  = crNS + ":cluster-cascade"
)

// seedCascadeFixture builds a data centre with one rack, one server and one
// network device, the server carrying a NIC, an SCP and an OOB IP.
//
// Deliberately includes the three children a server delete used to ORPHAN and
// the one a data-centre delete used to orphan — a fixture that only held the
// children the old code handled could not have failed.
func seedCascadeFixture(t *testing.T) {
	t.Helper()
	crGQL(t, `mutation($i:[AddDataCenterInput!]!){ addDataCenter(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeDC, "name": "cascade dc", "version": 1}}})
	crGQL(t, `mutation($i:[AddRackInput!]!){ addRack(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeRack, "name": "cascade rack", "version": 1,
			"dataCenter": map[string]any{"orbId": cascadeDC}}}})
	crGQL(t, `mutation($i:[AddIPAddressInput!]!){ addIPAddress(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeIP, "name": "10.9.9.9", "address": "10.9.9.9", "version": 1}}})
	crGQL(t, `mutation($i:[AddServerInput!]!){ addServer(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeServer, "name": "cascade server", "version": 1,
			"hostname":   "cascade-host",
			"dataCenter": map[string]any{"orbId": cascadeDC},
			"rack":       map[string]any{"orbId": cascadeRack},
			"oobIP":      map[string]any{"orbId": cascadeIP}}}})
	crGQL(t, `mutation($i:[AddNetworkAdapterInput!]!){ addNetworkAdapter(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeNIC, "name": "NIC.1", "version": 1,
			"server": map[string]any{"orbId": cascadeServer}}}})
	crGQL(t, `mutation($i:[AddServerConfigurationProfileInput!]!){ addServerConfigurationProfile(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeSCP, "name": "scp", "version": 1,
			"server": map[string]any{"orbId": cascadeServer}}}})
	// The node is created NESTED inside a cluster, which is the only way:
	// KubernetesNode.cluster is REQUIRED and typed by the KubernetesCluster
	// INTERFACE, and DGraph generates no ref input for an interface-typed field.
	crGQL(t, `mutation($i:[AddEksaKubernetesClusterInput!]!){ addEksaKubernetesCluster(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeClust, "name": "cascade cluster", "version": 1,
			"dataCenter": map[string]any{"orbId": cascadeDC},
			"nodes": []any{map[string]any{
				"namespace": crNS, "orbId": cascadeNode, "name": "cascade node", "version": 1,
				"server": map[string]any{"orbId": cascadeServer}}}}}})
	crGQL(t, `mutation($i:[AddNetworkDeviceInput!]!){ addNetworkDevice(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeNetDev, "name": "cascade switch", "version": 1,
			"dataCenter": map[string]any{"orbId": cascadeDC}}}})
	t.Cleanup(func() {
		for _, e := range [][2]string{
			{"KubernetesNode", cascadeNode}, {"EksaKubernetesCluster", cascadeClust},
			{"NetworkAdapter", cascadeNIC}, {"ServerConfigurationProfile", cascadeSCP},
			{"NetworkDevice", cascadeNetDev}, {"Server", cascadeServer},
			{"Rack", cascadeRack}, {"IPAddress", cascadeIP}, {"DataCenter", cascadeDC},
		} {
			deleteEntity(t, e[0], e[1])
		}
	})
}

// Criterion 1 — an editable member is deleted with its parent, transitively.
func TestCascade_FollowsOwnedMembersTransitively(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)

	if rec := deleteReq(t, h, "DataCenter", cascadeDC, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	// The data centre's editable members, and THEIR editable members: the walk
	// has to reach the NIC two levels down, not just the server one level down.
	for _, gone := range [][2]string{
		{"DataCenter", cascadeDC}, {"Rack", cascadeRack},
		{"Server", cascadeServer}, {"NetworkAdapter", cascadeNIC},
	} {
		if exists(t, gone[0], gone[1]) {
			t.Errorf("%s %s survived; it is inside the data centre's unit", gone[0], gone[1])
		}
	}
}

// Criterion 2 — a non-editable member survives, and is NAMED in the preview.
//
// Both halves. Surviving silently is how an operator discovers after the fact
// that something they expected to go is still there.
func TestCascade_NonOwnedMemberIsPreservedAndSaidSo(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)
	ctx := context.Background()

	plan, err := h.planFor(ctx, "Server", cascadeServer)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var preservedText string
	for _, g := range plan.preview.Preserved {
		preservedText += g.Label + " " + strings.Join(g.Items, " ")
	}
	for _, want := range []string{"Ip Addresses", "Racks", "Data Centers"} {
		if !strings.Contains(preservedText, want) {
			t.Errorf("preview must name %q as preserved; got %q", want, preservedText)
		}
	}

	if rec := deleteReq(t, h, "Server", cascadeServer, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	for _, kept := range [][2]string{
		{"IPAddress", cascadeIP}, {"Rack", cascadeRack}, {"DataCenter", cascadeDC},
	} {
		if !exists(t, kept[0], kept[1]) {
			t.Errorf("%s %s was deleted; it is not part of the server's unit", kept[0], kept[1])
		}
	}
}

// Criterion 6 — deleting a server leaves no orphans.
//
// Three live defects in one test, every one of them a child holding a NON-NULL
// edge to a node that no longer existed. DGraph propagates a missing non-null
// field to the ROOT of any query that selects it, so each was one delete away
// from taking out every query that walked there.
func TestCascade_ServerDeleteLeavesNoOrphans(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)

	if rec := deleteReq(t, h, "Server", cascadeServer, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	// NetworkAdapter.server is `Server!` — it cannot exist without one.
	if exists(t, "NetworkAdapter", cascadeNIC) {
		t.Error("the server's NIC survived, holding a non-null edge to a deleted server")
	}
	// KubernetesNode.server is `Server!` too. The old path CLEARED that edge
	// instead of removing the node, which left a node whose required `server`
	// resolved to nothing — the same failure by a different route.
	if exists(t, "KubernetesNode", cascadeNode) {
		t.Error("the server's KubernetesNode survived, holding a non-null edge to a deleted server")
	}
	// …and the cluster it belonged to is NOT the server's to delete.
	if !exists(t, "EksaKubernetesCluster", cascadeClust) {
		t.Error("deleting a server destroyed its node's cluster; a cluster is not part of a server's unit")
	}
	assertNoEdgeTo(t, "EksaKubernetesCluster", cascadeClust, "nodes")
	// ServerConfigurationProfile.server is nullable as of v13, and the SCP is
	// not a member of the Server view, so it legitimately survives — but then
	// NOTHING may be left pointing at the dead server.
	if exists(t, "ServerConfigurationProfile", cascadeSCP) {
		assertNoEdgeTo(t, "ServerConfigurationProfile", cascadeSCP, "server")
	}
}

// Criterion 7 — deleting a data centre removes its network devices.
//
// `NetworkDevice.dataCenter` is `DataCenter!`, and no cascade touched them: a
// data-centre delete left every switch in it pointing at a tombstone.
func TestCascade_DataCenterDeleteRemovesNetworkDevices(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)

	if rec := deleteReq(t, h, "DataCenter", cascadeDC, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if exists(t, "NetworkDevice", cascadeNetDev) {
		t.Error("the data centre's network device survived, holding a non-null edge to a deleted data centre")
	}
}

// Criterion 8 — a shared IPAddress survives every delete that reaches it.
//
// The same edge used to have opposite answers depending on which page you
// clicked Delete on: a server delete preserved the IP, a data-centre delete
// destroyed it. An IPAddress is claimed by four types, so no single parent owns
// it — the same reasoning that makes it render as a link rather than inline.
func TestCascade_SharedIPAddressSurvivesEveryParent(t *testing.T) {
	for _, root := range []struct{ typeName, orbID string }{
		{"Server", cascadeServer},
		{"DataCenter", cascadeDC},
	} {
		t.Run(root.typeName, func(t *testing.T) {
			h, _ := deleteFixture(t)
			seedCascadeFixture(t)
			if rec := deleteReq(t, h, root.typeName, root.orbID, "", "admin"); rec.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
			}
			if !exists(t, "IPAddress", cascadeIP) {
				t.Errorf("deleting a %s destroyed a shared IP address; no single parent owns one",
					root.typeName)
			}
		})
	}
}

// Criterion 4 — a survivor's edge into the deleted set is cleared.
//
// Derived, not listed: `@hasInverse` tells orbital which field points back, so
// there is no hand-maintained table to forget an entry in. A DQL delete does not
// maintain `@hasInverse`, and orbital's cascade is a DQL upsert deliberately —
// it is the only way to get a version-guarded CAS.
func TestCascade_ClearsEdgesHeldBySurvivors(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)

	if rec := deleteReq(t, h, "Server", cascadeServer, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	// The export subgraph query's shape: walk the survivor's list edge and
	// select a non-nullable field through it. This is what breaks.
	for _, survivor := range []struct{ typeName, orbID, edge string }{
		{"DataCenter", cascadeDC, "servers"},
		{"Rack", cascadeRack, "servers"},
	} {
		assertNoEdgeTo(t, survivor.typeName, survivor.orbID, survivor.edge)
	}
}

// Criterion 5 — the preview and the delete are built from one walk.
//
// Two walks would be two chances to disagree, and the disagreement would be
// invisible: an operator confirms a dialog describing one set and a different
// set is removed.
func TestCascade_PreviewAndDeleteAgree(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)
	ctx := context.Background()

	plan, err := h.planFor(ctx, "DataCenter", cascadeDC)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.preview.TotalCount != len(plan.uids) {
		t.Errorf("the preview says %d and the delete would remove %d",
			plan.preview.TotalCount, len(plan.uids))
	}
	rec := deleteReq(t, h, "DataCenter", cascadeDC, "", "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"deleted":`+itoa(plan.preview.TotalCount)) {
		t.Errorf("the delete reported a different count from the preview's %d: %s",
			plan.preview.TotalCount, rec.Body.String())
	}
}

// assertNoEdgeTo runs the shape that actually breaks: walk the edge and select a
// non-nullable field through it. A dangling edge fails the WHOLE query, which is
// why this asserts on `errors` rather than on the row count.
func assertNoEdgeTo(t *testing.T, typeName, orbID, edge string) {
	t.Helper()
	raw := crGQLRaw(t, `query($orbId: String!) {
	  query`+typeName+`(filter: { orbId: { eq: $orbId } }) {
	    orbId
	    `+edge+` { ... on ConfigItem { orbId } }
	  }
	}`, map[string]any{"orbId": orbID})
	if strings.Contains(string(raw), "Non-nullable field") {
		t.Errorf("%s %s still points at a deleted node through %s — "+
			"the whole query fails, which is how one delete breaks export for a data centre: %s",
			typeName, orbID, edge, raw)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
