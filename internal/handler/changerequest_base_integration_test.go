//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
)

// The base hash is the whole staleness mechanism. Three behaviours it has to
// have, each of which silently breaks a different guarantee if wrong:
//
//   - an owned child changing must move the parent's hash (else a reviewer's
//     approval of a Server survives someone editing its iDRAC)
//   - a declared-but-absent orbId must be in scope (else creating that entity
//     mid-review is invisible and the create silently becomes an overwrite)
//   - an unrelated entity changing must NOT move the hash (else every change
//     request in a namespace goes stale on every write, and staleness stops
//     meaning anything)

const (
	cbDC     = "cr-base:datacenter-1"
	cbServer = "cr-base:server-CRBASE1"
	cbIdrac  = "cr-base:idrac-CRBASE1"
	cbOther  = "cr-base:server-UNRELATED"
)

func TestBaseSnapshot_ScopeAndHash(t *testing.T) {
	ctx := context.Background()
	url := testutil.DGraphURL()
	seedBaseFixture(t)

	existing, err := approval.NewDGraphSchemaSource(url).ResolveEntities(ctx, []string{cbServer})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// The owned child is pulled in without the caller naming it.
	scope := baseScope(ctx, url, []string{cbServer}, existing)
	if !contains(scope, cbServer) || !contains(scope, cbIdrac) {
		t.Fatalf("scope = %v, want it to contain the server and its idrac", scope)
	}
	if contains(scope, cbOther) {
		t.Fatalf("scope = %v, must not reach an unrelated server", scope)
	}

	snap, err := baseSnapshot(ctx, url, scope)
	if err != nil {
		t.Fatalf("baseSnapshot: %v", err)
	}
	h0 := snap.ContentHash()
	if h0 == "" {
		t.Fatal("empty content hash")
	}
	if present := presentIn(snap, scope); len(present) != len(scope) {
		t.Errorf("present = %v, want all of scope %v", present, scope)
	}

	// (1) The owned child moves the parent's hash.
	setIdracFirmware(t, "9.9.9")
	snap, err = baseSnapshot(ctx, url, scope)
	if err != nil {
		t.Fatalf("baseSnapshot after child edit: %v", err)
	}
	if snap.ContentHash() == h0 {
		t.Error("editing an owned child did not move the hash — approvals would survive it")
	}
	setIdracFirmware(t, "1.0.0")
	snap, err = baseSnapshot(ctx, url, scope)
	if err != nil {
		t.Fatalf("baseSnapshot after revert: %v", err)
	}
	if snap.ContentHash() != h0 {
		t.Error("hash did not return to baseline after reverting the child")
	}

	// (2) An unrelated entity does not.
	gqlMutate(t, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": "cr-base", "orbId": cbOther, "version": 1, "hostname": "moved",
			"dataCenter": map[string]any{"orbId": cbDC},
		}}})
	snap, err = baseSnapshot(ctx, url, scope)
	if err != nil {
		t.Fatalf("baseSnapshot after unrelated edit: %v", err)
	}
	if snap.ContentHash() != h0 {
		t.Error("an unrelated entity moved the hash — every request would go stale on every write")
	}
}

func TestBaseSnapshot_AbsentOrbIDStaysInScopeAndDetectsCreate(t *testing.T) {
	ctx := context.Background()
	url := testutil.DGraphURL()
	seedBaseFixture(t)

	const willExist = "cr-base:datacenter-created-later"

	// Declared but absent: no entity, so nothing resolves.
	existing, err := approval.NewDGraphSchemaSource(url).ResolveEntities(ctx, []string{willExist})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(existing) != 0 {
		t.Fatalf("fixture is dirty — %s already exists", willExist)
	}

	scope := baseScope(ctx, url, []string{willExist}, existing)
	if len(scope) != 1 || scope[0] != willExist {
		t.Fatalf("scope = %v, want the declared orbId even though it does not exist", scope)
	}

	snap, err := baseSnapshot(ctx, url, scope)
	if err != nil {
		t.Fatalf("baseSnapshot: %v", err)
	}
	h0 := snap.ContentHash()
	if got := presentIn(snap, scope); len(got) != 0 {
		t.Errorf("present = %v, want empty — nothing exists yet", got)
	}

	// Someone creates it during review. The hash must move, with no hook and no
	// event: the entity simply starts matching the scope query.
	gqlMutate(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": "cr-base", "orbId": willExist, "name": "raced", "version": 1,
		}}})
	t.Cleanup(func() { deleteByOrbID(t, "DataCenter", willExist) })

	snap, err = baseSnapshot(ctx, url, scope)
	if err != nil {
		t.Fatalf("baseSnapshot after create: %v", err)
	}
	if snap.ContentHash() == h0 {
		t.Fatal("an entity appearing in scope did not move the hash — a create would silently become an overwrite")
	}
	if got := presentIn(snap, scope); len(got) != 1 || got[0] != willExist {
		t.Errorf("present = %v, want [%s]", got, willExist)
	}
}

func TestFetchOrbIDSubgraph_EmptyScope(t *testing.T) {
	nodes, err := fetchOrbIDSubgraph(context.Background(), testutil.DGraphURL(), nil)
	if err != nil {
		t.Fatalf("fetchOrbIDSubgraph(nil): %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("got %d nodes, want 0", len(nodes))
	}
}

// The scope query has to carry edges, not just scalars — an edge repoint is a
// real change a reviewer must re-review, and expand(_all_) alone would miss it
// because DGraph drops cyclic uid predicates from it.
func TestFetchOrbIDSubgraph_IncludesEdges(t *testing.T) {
	seedBaseFixture(t)

	nodes, err := fetchOrbIDSubgraph(context.Background(), testutil.DGraphURL(), []string{cbServer})
	if err != nil {
		t.Fatalf("fetchOrbIDSubgraph: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	if _, ok := nodes[0]["Server.dataCenter"]; !ok {
		var keys []string
		for k := range nodes[0] {
			keys = append(keys, k)
		}
		t.Errorf("edge predicate Server.dataCenter absent; keys = %v", keys)
	}
}

func seedBaseFixture(t *testing.T) {
	t.Helper()
	gqlMutate(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": "cr-base", "orbId": cbDC, "name": "cr base fixture", "version": 1,
		}}})
	gqlMutate(t, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{
			map[string]any{
				"namespace": "cr-base", "orbId": cbServer, "version": 1, "hostname": "edge-01",
				"dataCenter": map[string]any{"orbId": cbDC},
			},
			map[string]any{
				"namespace": "cr-base", "orbId": cbOther, "version": 1, "hostname": "unrelated",
				"dataCenter": map[string]any{"orbId": cbDC},
			},
		}})
	// The child gets its OWN mutation, and it is not optional. Nesting
	// idracSettings under the Server would only LINK — the child's field values
	// are discarded, so firmwareVersion would never be written and the "editing
	// an owned child moves the hash" assertion below would pass vacuously.
	// The link comes back the other way for free via @hasInverse.
	gqlMutate(t, `mutation($input:[AddIdracSettingsInput!]!){ addIdracSettings(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": "cr-base", "orbId": cbIdrac, "version": 1, "firmwareVersion": "1.0.0",
			"server": map[string]any{"orbId": cbServer},
		}}})

	t.Cleanup(func() {
		deleteByOrbID(t, "IdracSettings", cbIdrac)
		deleteByOrbID(t, "Server", cbServer)
		deleteByOrbID(t, "Server", cbOther)
		deleteByOrbID(t, "DataCenter", cbDC)
	})
}

func setIdracFirmware(t *testing.T, v string) {
	t.Helper()
	gqlMutate(t, `mutation($orbId:String!,$set:IdracSettingsPatch!){ updateIdracSettings(input:{filter:{orbId:{eq:$orbId}}, set:$set}){ numUids } }`,
		map[string]any{"orbId": cbIdrac, "set": map[string]any{"firmwareVersion": v}})
}

func deleteByOrbID(t *testing.T, typeName, orbID string) {
	t.Helper()
	gqlMutate(t, `mutation($orbId:String!){ delete`+typeName+`(filter:{orbId:{eq:$orbId}}){ numUids } }`,
		map[string]any{"orbId": orbID})
}

func gqlMutate(t *testing.T, query string, vars map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(testutil.DGraphURL(), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post dgraph: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || bytes.Contains(out, []byte(`"errors"`)) {
		t.Fatalf("dgraph %d: %s", resp.StatusCode, out)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
