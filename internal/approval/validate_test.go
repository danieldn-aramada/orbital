package approval

import (
	"context"
	"strings"
	"testing"
)

// fakeSchema is a hand-built SchemaSource so the RULES can be tested without
// DGraph. The real introspection path is covered separately in
// schema_integration_test.go — these two together mean a rule change and a
// schema-shape change each fail their own test.
type fakeSchema struct {
	entities map[string]EntityRef
	types    map[string]TypeSchema
}

func (f fakeSchema) ResolveEntities(_ context.Context, ids []string) (map[string]EntityRef, error) {
	out := map[string]EntityRef{}
	for _, id := range ids {
		if e, ok := f.entities[id]; ok {
			out[id] = e
		}
	}
	return out, nil
}

func (f fakeSchema) NamespaceExists(_ context.Context, name string) (bool, error) {
	for _, e := range f.entities {
		if e.Namespace == name {
			return true, nil
		}
	}
	return false, nil
}

func (f fakeSchema) TypeSchemas(_ context.Context, names []string) (map[string]TypeSchema, error) {
	out := map[string]TypeSchema{}
	for _, n := range names {
		if t, ok := f.types[n]; ok {
			out[n] = t
		}
	}
	return out, nil
}

func testSource() fakeSchema {
	return fakeSchema{
		entities: map[string]EntityRef{
			"ns:server-A":  {OrbID: "ns:server-A", Type: "Server", Namespace: "ns"},
			"ns:idrac-A":   {OrbID: "ns:idrac-A", Type: "IdracSettings", Namespace: "ns"},
			"other:server": {OrbID: "other:server", Type: "Server", Namespace: "other"},
			// Edge TARGETS — referenced by set values, never themselves changed.
			"ns:dc-1":  {OrbID: "ns:dc-1", Type: "DataCenter", Namespace: "ns"},
			"ns:nic-1": {OrbID: "ns:nic-1", Type: "NetworkAdapter", Namespace: "ns"},
			"ns:nic-2": {OrbID: "ns:nic-2", Type: "NetworkAdapter", Namespace: "ns"},
		},
		types: map[string]TypeSchema{
			"Server": {
				Fields: map[string]FieldSchema{
					"hostname":      {TypeName: "String"},
					"rackPosition":  {TypeName: "Int"},
					"version":       {TypeName: "Int"},
					"namespace":     {TypeName: "String"},
					"dataCenter":    {IsEdge: true, TypeName: "DataCenterRef"},
					"idracSettings": {IsEdge: true, TypeName: "IdracSettingsRef"},
					"networkAdapters": {
						IsEdge: true, IsList: true, TypeName: "NetworkAdapterRef",
					},
				},
				RequiredOnCreate: []string{"dataCenter"},
			},
			"IdracSettings": {
				Fields: map[string]FieldSchema{
					"firmwareVersion": {TypeName: "String"},
					"sshEnabled":      {TypeName: "Boolean"},
				},
			},
			"DataCenter": {
				Fields: map[string]FieldSchema{
					"name":        {TypeName: "String"},
					"assetDataV2": {TypeName: "String"}, // declared String, holds JSON
				},
			},
		},
	}
}

func validate(t *testing.T, cs Changeset) ValidationResult {
	t.Helper()
	res, err := Validate(context.Background(), testSource(), &cs)
	if err != nil {
		t.Fatalf("Validate returned a transport error: %v", err)
	}
	return res
}

func TestValidate_Accepts(t *testing.T) {
	tests := []struct {
		name string
		cs   Changeset
	}{
		{
			name: "update with type omitted — resolved from orbId",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"hostname": "edge-01"}},
			}},
		},
		{
			name: "clear a field",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Clear: []string{"hostname"}},
			}},
		},
		{
			name: "create carries type and every required field",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-NEW", Type: "Server", Op: OpUpsert, Set: map[string]any{
					"hostname":   "edge-02",
					"dataCenter": map[string]any{"orbId": "ns:dc-1"},
				}},
			}},
		},
		{
			name: "parent and owned child as separate items",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"hostname": "edge-01"}},
				{OrbID: "ns:idrac-A", Op: OpUpdate, Set: map[string]any{"firmwareVersion": "9.9.9"}},
			}},
		},
		{
			name: "list edge of references",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{
					"networkAdapters": []any{
						map[string]any{"orbId": "ns:nic-1"},
						map[string]any{"orbId": "ns:nic-2"},
					},
				}},
			}},
		},
		{
			name: "an item may reference an entity an EARLIER item creates",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:dc-new", Type: "DataCenter", Op: OpUpsert, Set: map[string]any{"name": "new dc"}},
				{OrbID: "ns:server-NEW", Type: "Server", Op: OpUpsert, Set: map[string]any{
					"hostname": "edge-03", "dataCenter": map[string]any{"orbId": "ns:dc-new"},
				}},
			}},
		},
		{
			name: "delete an existing entity",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpDelete},
			}},
		},
		{
			name: "delete an absent entity is an idempotent no-op",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-GONE", Op: OpDelete},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := validate(t, tt.cs)
			if len(res.Errors) > 0 {
				t.Fatalf("expected no errors, got: %v", res.Errors)
			}
		})
	}
}

func TestValidate_Rejects(t *testing.T) {
	tests := []struct {
		name      string
		cs        Changeset
		wantMatch string
	}{
		{
			name: "nested owned-child fields under an edge are silently discarded by DGraph",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{
					"idracSettings": map[string]any{"firmwareVersion": "9.9.9"},
				}},
			}},
			wantMatch: "silently discarded",
		},
		{
			name: "edge reference mixing an identity key with a field",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{
					"dataCenter": map[string]any{"orbId": "ns:dc-1", "name": "renamed"},
				}},
			}},
			wantMatch: "silently discarded",
		},
		{
			name: "unknown field",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"hostnmae": "typo"}},
			}},
			wantMatch: "no such field",
		},
		{
			name: "unknown field in clear",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Clear: []string{"nope"}},
			}},
			wantMatch: "no such field",
		},
		{
			name: "orbital-stamped field",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"version": 99}},
			}},
			wantMatch: "set by orbital",
		},
		{
			name: "namespace cannot be reassigned through a change request",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"namespace": "other"}},
			}},
			wantMatch: "set by orbital",
		},
		{
			name: "same field in set and clear",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate,
					Set: map[string]any{"hostname": "x"}, Clear: []string{"hostname"}},
			}},
			wantMatch: "both set and clear",
		},
		{
			name: "cross-namespace item",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "other:server", Op: OpUpdate, Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "declares",
		},
		{
			name: "malformed orbId",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "no-namespace-prefix", Op: OpUpdate, Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "<namespace>:<key>",
		},
		{
			name: "update against an entity that does not exist",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-GONE", Op: OpUpdate, Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "requires an existing entity",
		},
		{
			name: "create without a type",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-NEW", Op: OpUpsert, Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "type is required",
		},
		{
			name: "create missing a required field",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-NEW", Type: "Server", Op: OpUpsert, Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "requires dataCenter",
		},
		{
			name: "declared type contradicts the entity in the graph",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Type: "IdracSettings", Op: OpUpdate,
					Set: map[string]any{"firmwareVersion": "9.9.9"}},
			}},
			wantMatch: "does not match the existing entity",
		},
		{
			name: "unknown op",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: "patch", Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "unknown op",
		},
		{
			name: "missing op",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "op is required",
		},
		{
			name: "delete carrying a set",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpDelete, Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "must not carry set or clear",
		},
		{
			name: "update with neither set nor clear",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate},
			}},
			wantMatch: "requires set or clear",
		},
		{
			name: "two items on the same entity make the outcome order-dependent",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"hostname": "a"}},
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"hostname": "b"}},
			}},
			wantMatch: "duplicate orbId",
		},
		{
			name: "edge pointing at an orbId that does not exist",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{
					"dataCenter": map[string]any{"orbId": "ns:dc-typo"},
				}},
			}},
			wantMatch: "does not exist",
		},
		{
			name: "edge pointing at an entity created LATER in the same changeset",
			cs: Changeset{Namespace: "ns", Changes: []ChangeItem{
				{OrbID: "ns:server-NEW", Type: "Server", Op: OpUpsert, Set: map[string]any{
					"hostname": "edge-03", "dataCenter": map[string]any{"orbId": "ns:dc-later"},
				}},
				{OrbID: "ns:dc-later", Type: "DataCenter", Op: OpUpsert, Set: map[string]any{"name": "too late"}},
			}},
			wantMatch: "does not exist",
		},
		{
			name:      "empty changeset",
			cs:        Changeset{Namespace: "ns"},
			wantMatch: "must not be empty",
		},
		{
			name: "missing namespace",
			cs: Changeset{Changes: []ChangeItem{
				{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"hostname": "x"}},
			}},
			wantMatch: "namespace is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := validate(t, tt.cs)
			if len(res.Errors) == 0 {
				t.Fatalf("expected an error matching %q, got none", tt.wantMatch)
			}
			var joined []string
			for _, e := range res.Errors {
				joined = append(joined, e.Error()+" | "+e.Hint)
			}
			all := strings.Join(joined, "\n")
			if !strings.Contains(all, tt.wantMatch) {
				t.Errorf("no error matched %q; got:\n%s", tt.wantMatch, all)
			}
		})
	}
}

// A JSON-valued String column sent as structure is rejected here rather than
// by DGraph, whose "cannot use as String" surfaces at merge — long after the
// reviewer approved it.
func TestValidate_JSONStringFieldSentAsObject(t *testing.T) {
	src := testSource()
	src.entities["ns:dc-1"] = EntityRef{OrbID: "ns:dc-1", Type: "DataCenter", Namespace: "ns"}
	res, err := Validate(context.Background(), src, &Changeset{
		Namespace: "ns",
		Changes: []ChangeItem{
			{OrbID: "ns:dc-1", Op: OpUpdate, Set: map[string]any{
				"assetDataV2": map[string]any{"rack": "R1"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Msg, "expected a String value") {
		t.Fatalf("want a String-value error, got %v", res.Errors)
	}
}

// base_present must list exactly the declared orbIds that exist, because the
// merge path uses it to tell a create (absent at open) from a deleted target
// (present at open) — getting it wrong turns a hard failure into a silent
// partial recreate.
func TestValidate_PresentIsExactlyWhatExists(t *testing.T) {
	res := validate(t, Changeset{Namespace: "ns", Changes: []ChangeItem{
		{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"hostname": "x"}},
		{OrbID: "ns:server-NEW", Type: "Server", Op: OpUpsert, Set: map[string]any{
			"hostname": "y", "dataCenter": map[string]any{"orbId": "ns:dc-1"},
		}},
	}})
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if len(res.Present) != 1 || res.Present[0] != "ns:server-A" {
		t.Errorf("Present = %v, want [ns:server-A]", res.Present)
	}
}

// Every problem in one response — a client should not have to fix errors one
// round-trip at a time.
func TestValidate_ReportsEveryProblem(t *testing.T) {
	res := validate(t, Changeset{Namespace: "ns", Changes: []ChangeItem{
		{OrbID: "ns:server-A", Op: OpUpdate, Set: map[string]any{"bogus": 1}},
		{OrbID: "bad-orbid", Op: OpUpdate, Set: map[string]any{"hostname": "x"}},
		{OrbID: "ns:server-B", Op: OpUpdate, Set: map[string]any{"hostname": "x"}},
	}})
	if len(res.Errors) != 3 {
		t.Fatalf("want 3 errors, got %d: %v", len(res.Errors), res.Errors)
	}
	for i, e := range res.Errors {
		if e.Index != i {
			t.Errorf("errors are not ordered by item index: %v", res.Errors)
			break
		}
	}
}
