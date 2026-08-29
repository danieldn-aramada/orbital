//go:build integration

package approval_test

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

// These exercise the one thing the unit tests deliberately fake: that
// DGraphSchemaSource reads the shapes it claims to read from a REAL deployed
// schema. The regression class is DGraph changing its introspection shape (or
// the schema gaining a construct the three-level unwrap can't see) — the
// validator would then either pass everything or reject everything, and no
// unit test with a hand-built source would notice.

const dcOrbID = "approval-it:datacenter-schema-source"

func TestDGraphSchemaSource_TypeSchemas(t *testing.T) {
	src := approval.NewDGraphSchemaSource(testutil.DGraphURL())

	got, err := src.TypeSchemas(context.Background(), []string{"Server", "IdracSettings", "NotARealType"})
	if err != nil {
		t.Fatalf("TypeSchemas: %v", err)
	}

	if _, ok := got["NotARealType"]; ok {
		t.Error("an unknown type was returned as if it existed")
	}

	server, ok := got["Server"]
	if !ok {
		t.Fatal("Server missing — is the schema deployed? (make up && make seed)")
	}

	// A scalar, an object edge, and a list edge: the three shapes the unwrap
	// has to distinguish for the nested-write rule to work at all.
	if f, ok := server.Fields["hostname"]; !ok || f.IsEdge || f.TypeName != "String" {
		t.Errorf("Server.hostname = %+v, want a String scalar", f)
	}
	if f, ok := server.Fields["dataCenter"]; !ok || !f.IsEdge || f.IsList {
		t.Errorf("Server.dataCenter = %+v, want a single edge", f)
	}
	if f, ok := server.Fields["networkAdapters"]; !ok || !f.IsEdge || !f.IsList {
		t.Errorf("Server.networkAdapters = %+v, want a list edge", f)
	}

	// Aggregate/read-only fields exist on the Server TYPE but not on
	// ServerPatch. Introspecting the Patch input is what keeps them out — if
	// this ever fails, the source switched to the wrong introspection target
	// and clients could propose writes to fields DGraph will reject.
	if _, ok := server.Fields["storageControllersAggregate"]; ok {
		t.Error("an aggregate (query-only) field is being offered as settable")
	}
	if _, ok := server.Fields["id"]; ok {
		t.Error("id is being offered as settable")
	}

	// dataCenter is NON_NULL on AddServerInput — a Server cannot be created
	// without one, and the validator says so before a reviewer sees it.
	var hasDC bool
	for _, r := range server.RequiredOnCreate {
		if r == "dataCenter" {
			hasDC = true
		}
		if r == "namespace" || r == "orbId" || r == "version" {
			t.Errorf("RequiredOnCreate includes %q, which orbital stamps itself", r)
		}
	}
	if !hasDC {
		t.Errorf("RequiredOnCreate = %v, want it to include dataCenter", server.RequiredOnCreate)
	}

	if idrac, ok := got["IdracSettings"]; !ok {
		t.Error("IdracSettings missing")
	} else if f, ok := idrac.Fields["sshEnabled"]; !ok || f.IsEdge || f.TypeName != "Boolean" {
		t.Errorf("IdracSettings.sshEnabled = %+v, want a Boolean scalar", f)
	}
}

func TestDGraphSchemaSource_ResolveEntities(t *testing.T) {
	ctx := context.Background()
	addDataCenter(t, dcOrbID)
	t.Cleanup(func() { deleteDataCenter(t, dcOrbID) })

	src := approval.NewDGraphSchemaSource(testutil.DGraphURL())
	got, err := src.ResolveEntities(ctx, []string{dcOrbID, "approval-it:definitely-absent"})
	if err != nil {
		t.Fatalf("ResolveEntities: %v", err)
	}

	ref, ok := got[dcOrbID]
	if !ok {
		t.Fatalf("seeded entity not resolved; got %v", got)
	}
	// The type resolved from orbId alone is what makes `type` optional on
	// input — if this stops working, every change request must name its types.
	if ref.Type != "DataCenter" {
		t.Errorf("type = %q, want DataCenter", ref.Type)
	}
	if ref.Namespace != "approval-it" {
		t.Errorf("namespace = %q, want approval-it", ref.Namespace)
	}

	// An absent orbId must be OMITTED, not returned empty. Absence is the
	// signal that distinguishes a create from an update; an empty EntityRef
	// with Type "" would read as "exists but is typeless".
	if _, ok := got["approval-it:definitely-absent"]; ok {
		t.Error("an absent orbId came back as present")
	}
}

func TestDGraphSchemaSource_ResolveEntities_Empty(t *testing.T) {
	src := approval.NewDGraphSchemaSource(testutil.DGraphURL())
	got, err := src.ResolveEntities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveEntities(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// Validate against the real schema — the unit tests prove the rules, this
// proves the rules and the real field set agree. A field that exists in the
// registry but not in the deployed schema would pass every unit test.
func TestValidate_AgainstDeployedSchema(t *testing.T) {
	addDataCenter(t, dcOrbID)
	t.Cleanup(func() { deleteDataCenter(t, dcOrbID) })

	src := approval.NewDGraphSchemaSource(testutil.DGraphURL())

	res, err := approval.Validate(context.Background(), src, &approval.Changeset{
		Namespace: "approval-it",
		Changes: []approval.ChangeItem{
			{OrbID: dcOrbID, Op: approval.OpUpdate, Set: map[string]any{"name": "renamed"}},
		},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(res.Errors) > 0 {
		t.Fatalf("valid changeset rejected: %v", res.Errors)
	}
	if len(res.Present) != 1 || res.Present[0] != dcOrbID {
		t.Errorf("Present = %v, want [%s]", res.Present, dcOrbID)
	}

	// The nested-write trap, against the real schema this time.
	res, err = approval.Validate(context.Background(), src, &approval.Changeset{
		Namespace: "approval-it",
		Changes: []approval.ChangeItem{
			{OrbID: dcOrbID, Op: approval.OpUpdate, Set: map[string]any{
				"servers": []any{map[string]any{"hostname": "would-be-discarded"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("nested write not rejected: %v", res.Errors)
	}
}

func addDataCenter(t *testing.T, orbID string) {
	t.Helper()
	gql(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": "approval-it", "orbId": orbID, "name": "schema source fixture", "version": 1,
		}}})
}

func deleteDataCenter(t *testing.T, orbID string) {
	t.Helper()
	gql(t, `mutation($orbId:String!){ deleteDataCenter(filter:{orbId:{eq:$orbId}}){ numUids } }`,
		map[string]any{"orbId": orbID})
}

func gql(t *testing.T, query string, vars map[string]any) {
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
