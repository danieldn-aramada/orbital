package handler

import "testing"

// Orbital stamps version/createdBy/createdAt/updatedBy/updatedAt into every ADD
// input, because those fields must be unspoofable and consistent for every
// caller. They exist only on ConfigItem types.
//
// The regression guarded here is the stamper treating ANY array-of-maps
// variable as a config item — which it did until 2026-10-09. A mutation that
// adds something else in the same request (creating a data centre also upserts
// its Namespace, which has no version) was refused WHOLESALE by DGraph with
// "unknown field", so the config-item half never landed either. The failure is
// at the far end of the write path and reads as a schema problem, not as a
// proxy one.
func TestStampableAddVars(t *testing.T) {
	isConfigItem := func(name string) bool { return name == "DataCenter" || name == "Server" }

	cases := []struct {
		name  string
		query string
		want  map[string]bool
	}{
		{
			"a config item is stamped",
			`mutation Create($input: [AddServerInput!]!) { addServer(input: $input) { numUids } }`,
			map[string]bool{"input": true},
		},
		{
			"a non-config-item in the SAME request is not",
			`mutation Create($ns: [AddNamespaceInput!]!, $input: [AddDataCenterInput!]!) {
				addNamespace(input: $ns, upsert: true) { numUids }
				addDataCenter(input: $input) { numUids }
			}`,
			map[string]bool{"ns": false, "input": true},
		},
		{
			// The variable NAME carries no type information — callers use
			// `input`, `idracInput`, `ns`. Reading the declaration is the point.
			"the decision follows the declared type, not the variable name",
			`mutation Create($input: [AddNamespaceInput!]!) { addNamespace(input: $input) { numUids } }`,
			map[string]bool{"input": false},
		},
		{
			// Nothing declared this way → absent from the map → the caller
			// stamps, which is the behaviour every existing client relies on.
			"an undeclared shape is left to the old behaviour",
			`mutation { addServer(input: [{name: "x"}]) { numUids } }`,
			map[string]bool{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stampableAddVars(tc.query, isConfigItem)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("var %q: got %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

// The stamper itself must leave a non-ConfigItem input untouched and still
// stamp the config item beside it — asserted on the payload that actually goes
// to DGraph, because that is what gets refused.
func TestStampMutation_SkipsNonConfigItemInput(t *testing.T) {
	req := &gqlRequest{
		Query: `mutation Create($ns: [AddNamespaceInput!]!, $input: [AddDataCenterInput!]!) {
			addNamespace(input: $ns, upsert: true) { numUids }
			addDataCenter(input: $input) { numUids }
		}`,
		Variables: map[string]any{
			"ns":    []any{map[string]any{"name": "houston"}},
			"input": []any{map[string]any{"orbId": "houston:dc", "name": "dc"}},
		},
	}
	stampMutation(req, nil, "someone@example.com",
		func(name string) bool { return name == "DataCenter" })

	ns := req.Variables["ns"].([]any)[0].(map[string]any)
	for _, f := range []string{"version", "createdBy", "createdAt", "updatedBy", "updatedAt"} {
		if _, has := ns[f]; has {
			t.Errorf("Namespace input was stamped with %q; DGraph refuses the whole request for it", f)
		}
	}
	if ns["name"] != "houston" {
		t.Errorf("the Namespace input's own field was altered: %v", ns)
	}

	dc := req.Variables["input"].([]any)[0].(map[string]any)
	if dc["version"] != 1 {
		t.Errorf("the config item beside it must still be stamped: version = %v, want 1", dc["version"])
	}
	if dc["createdBy"] != "someone@example.com" {
		t.Errorf("createdBy = %v, want the actor", dc["createdBy"])
	}
}
