package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// ── Pure function tests ───────────────────────────────────────────────────────

func TestIsMutation(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  bool
	}{
		// Happy paths
		{"simple mutation", "mutation { addServer(input:[]) { server { id } } }", true},
		{"named mutation", "mutation Foo { updateDataCenter(input:{}) { dataCenter { id } } }", true},
		{"mutation with leading whitespace + newline", "  mutation\n{ deleteServer(filter:{}) { server { id } } }", true},
		{"uppercase MUTATION", "MUTATION Foo { updateDataCenter(input:{}) { dataCenter { id } } }", true},
		{"shorthand query", "{ queryDataCenter { id name } }", false},
		{"explicit query", "query { queryServer { id } }", false},
		{"empty body", "", false},

		// Bypass vectors that the original prefix-check missed
		{"#-comment hiding mutation", "# innocuous comment\nmutation Bar { addServer(input:[]) { server { id } } }", true},
		{"#-comment inline before mutation", "# what's this?\n# another line\nmutation Bar { ok }", true},
		{"query operation first, mutation second", "query Foo { ok }\nmutation Bar { addServer(input:[]) { server { id } } }", true},
		{"block string before mutation", `"""leading docblock"""` + "\nmutation Bar { addServer(input:[]) { server { id } } }", true},

		// Strings and comments that contain the word "mutation" but aren't mutations
		{"string literal containing 'mutation'", `{ queryServer(filter: { name: { eq: "mutation" } }) { id } }`, false},
		{"comment containing the word mutation", "# this query references mutation behavior\n{ queryServer { id } }", false},
		{"block string containing 'mutation'", `{ queryServer(filter: { name: { eq: """mutation""" } }) { id } }`, false},

		// Identifier that contains 'mutation' as substring should not trigger
		{"identifier containing mutation substring", "{ queryMutationLog { id } }", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMutation(tt.query); got != tt.want {
				t.Errorf("isMutation(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestHasGQLErrors(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{"no errors field", []byte(`{"data":{}}`), false},
		{"empty errors array", []byte(`{"data":{},"errors":[]}`), false},
		{"one error", []byte(`{"errors":[{"message":"fail"}]}`), true},
		{"multiple errors", []byte(`{"data":null,"errors":[{},{}]}`), true},
		{"malformed json", []byte(`not json`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasGQLErrors(tt.body); got != tt.want {
				t.Errorf("hasGQLErrors(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestExtractOperations(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantOps   []string
		wantTypes []string
	}{
		{
			name:      "single update",
			query:     `mutation { updateServer(input:{}) { server { id } } }`,
			wantOps:   []string{"updateServer"},
			wantTypes: []string{"Server"},
		},
		{
			name:      "add and delete — deduped ops",
			query:     `mutation { addServer(input:[]) { server { id } } deleteServer(filter:{}) { server { id } } }`,
			wantOps:   []string{"addServer", "deleteServer"},
			wantTypes: []string{"Server"},
		},
		{
			name:      "mixed types",
			query:     `mutation { updateDataCenter(input:{}) { dataCenter { id } } addServer(input:[]) { server { id } } }`,
			wantOps:   []string{"updateDataCenter", "addServer"},
			wantTypes: []string{"DataCenter", "Server"},
		},
		{
			name:      "no known type — empty",
			query:     `mutation { customOp { id } }`,
			wantOps:   nil,
			wantTypes: nil,
		},
		{
			// Operation name must NOT be picked up — only body field calls. The
			// idrac-only branch of the edit-server modal builds a query named
			// "UpdateIdracSettings" whose body calls addIdracSettings(upsert:true).
			// A regex that scans the whole query was logging both ops; we only
			// want the body call.
			name:      "operation name not scanned",
			query:     `mutation UpdateIdracSettings($v: Int!) { addIdracSettings(input: $v, upsert: true) { numUids } }`,
			wantOps:   []string{"addIdracSettings"},
			wantTypes: []string{"IdracSettings"},
		},
		{
			name:      "operation name with And — body wins",
			query:     `mutation UpdateServerAndIdrac { updateServer(input:{}) { server { id } } addIdracSettings(input:[]) { numUids } }`,
			wantOps:   []string{"updateServer", "addIdracSettings"},
			wantTypes: []string{"Server", "IdracSettings"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops, types := extractOperations(tt.query)
			if !stringSlicesMatch(ops, tt.wantOps) {
				t.Errorf("ops: got %v, want %v", ops, tt.wantOps)
			}
			if !stringSlicesMatch(types, tt.wantTypes) {
				t.Errorf("types: got %v, want %v", types, tt.wantTypes)
			}
		})
	}
}

// stringSlicesMatch checks that two slices have the same elements in any order.
func stringSlicesMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// ── Handler proxy tests ───────────────────────────────────────────────────────

// newGQLCtx builds an Echo context for a POST /graphql with the given JSON body.
func newGQLCtx(t *testing.T, body any) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	b, _ := json.Marshal(body)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// mockDGraph starts a server that echoes a fixed JSON response.
func mockDGraph(t *testing.T, response string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(response)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// captureRequests starts a server that captures all request bodies and echoes a fixed response.
func captureRequests(t *testing.T, response string) (*httptest.Server, *[][]byte) {
	t.Helper()
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(response)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func TestHandle_ProxyRawQuery(t *testing.T) {
	srv := mockDGraph(t, `{"data":{"queryDataCenter":[{"id":"dc1"}]}}`)
	h := NewGraphQL(srv.URL, nil, slog.Default(), false)

	c, rec := newGQLCtx(t, map[string]any{
		"query": `{ queryDataCenter { id } }`,
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "dc1") {
		t.Errorf("expected dc1 in response, got: %s", rec.Body.String())
	}
}

func TestHandle_MutationProxied(t *testing.T) {
	srv := mockDGraph(t, `{"data":{"addServer":{"server":[{"orbId":"alaska:SRV001"}]}}}`)
	h := NewGraphQL(srv.URL, nil, slog.Default(), false)

	c, rec := newGQLCtx(t, map[string]any{
		"query": `mutation { addServer(input:[]) { server { orbId } } }`,
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "alaska:SRV001") {
		t.Errorf("expected orbId in response, got: %s", rec.Body.String())
	}
}

// Replaced TestHandle_IfVersionStrippedBeforeProxy, which asserted that
// `ifVersion` was removed before forwarding. That is no longer the contract:
// with the version predicate injected into the query, `ifVersion` is DECLARED
// and referenced, so stripping it would send an undefined variable.
//
// What is worth pinning now is the other half — a mutation that cannot carry the
// precondition is REFUSED rather than having it quietly dropped. An `add` has no
// entity to match, so this is the shape a confused client actually sends.
func TestHandle_IfVersionOnAnAddIsRefusedNotSilentlyDropped(t *testing.T) {
	srv, bodies := captureRequests(t, `{"data":{}}`)
	h := NewGraphQL(srv.URL, nil, slog.Default(), false)

	c, rec := newGQLCtx(t, map[string]any{
		"query":     `mutation { addServer(input:[]) { server { id } } }`,
		"variables": map[string]any{"ifVersion": 5},
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — a precondition that cannot be applied was accepted: %s", rec.Code, rec.Body.String())
	}
	if len(*bodies) != 0 {
		t.Errorf("the mutation reached DGraph anyway (%d bodies) — refused, then sent", len(*bodies))
	}
}

func TestHandle_MVCCConflict(t *testing.T) {
	// DGraph returns before-state with version=5; client sends ifVersion=3 → conflict.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "BeforeFetch") {
			// Respond to the before-fetch with version=5
			w.Write([]byte(`{"data":{"getServer":{"id":"1","orbId":"alaska:SRV001","version":5}}}`)) //nolint:errcheck
		} else {
			w.Write([]byte(`{"data":{}}`)) //nolint:errcheck
		}
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)

	c, rec := newGQLCtx(t, map[string]any{
		"query":         `mutation UpdateServer { updateServer(input:{}) { server { id } } }`,
		"operationName": "UpdateServer",
		"variables": map[string]any{
			"id":        "1",
			"ifVersion": 3, // client thinks it's version 3, server has version 5
		},
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandle_MVCCVersionMatch(t *testing.T) {
	// Before-state version=5, ifVersion=5 → no conflict, mutation proceeds.
	//
	// Uses the canonical `$orbId` form because that is the only shape the
	// version predicate can be injected into — the inline fixture this used to
	// carry is now refused, which is the fail-closed guard working.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "BeforeFetch") {
			w.Write([]byte(`{"data":{"queryServer":[{"id":"1","orbId":"alaska:SRV001","version":5}]}}`)) //nolint:errcheck
		} else {
			w.Write([]byte(`{"data":{"updateServer":{"server":[{"orbId":"alaska:SRV001"}]}}}`)) //nolint:errcheck
		}
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)

	c, rec := newGQLCtx(t, map[string]any{
		"query":         `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { server { orbId } } }`,
		"operationName": "UpdateServer",
		"variables": map[string]any{
			"orbId": "alaska:SRV001", "set": map[string]any{"hostname": "x"}, "ifVersion": 5,
		},
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code == http.StatusConflict {
		t.Errorf("expected no conflict when versions match: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "alaska:SRV001") {
		t.Errorf("expected mutation response, got: %s", rec.Body.String())
	}
}

func TestHandle_MalformedIfVersionIsBadInputNotConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "BeforeFetch") {
			w.Write([]byte(`{"data":{"getServer":{"id":"1","version":5}}}`)) //nolint:errcheck
			return
		}
		t.Error("the mutation reached DGraph despite a malformed ifVersion")
		w.Write([]byte(`{"data":{}}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)
	c, rec := newGQLCtx(t, map[string]any{
		"query":         `mutation UpdateServer { updateServer(input:{}) { server { id } } }`,
		"operationName": "UpdateServer",
		"variables":     map[string]any{"id": "1", "ifVersion": "not-a-number"},
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — a malformed token must not be reported as a conflict: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), CodeBadUserInput) {
		t.Errorf("body does not carry %s: %s", CodeBadUserInput, rec.Body.String())
	}
}

func TestHandle_AutoIncrementVersionOnUpdate(t *testing.T) {
	// Before-state version=7. Caller omits version from `set`. Proxy must inject set.version=8.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "BeforeFetch") {
			w.Write([]byte(`{"data":{"getServer":{"id":"1","version":7}}}`)) //nolint:errcheck
			return
		}
		// Forwarded mutation: assert set.version == 8.
		var fwd gqlRequest
		_ = json.Unmarshal(body, &fwd)
		setMap, _ := fwd.Variables["set"].(map[string]any)
		v, _ := setMap["version"].(float64)
		if int(v) != 8 {
			t.Errorf("expected auto-injected set.version=8, got %v (set=%v)", setMap["version"], setMap)
		}
		w.Write([]byte(`{"data":{"updateServer":{"server":{"orbId":"x","version":8}}}}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)
	c, _ := newGQLCtx(t, map[string]any{
		"query":         `mutation UpdateServer($set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: "x" } }, set: $set }) { server { orbId version } } }`,
		"operationName": "UpdateServer",
		"variables": map[string]any{
			"id":  "1",
			"set": map[string]any{"hostname": "newname"},
		},
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestHandle_AutoIncrementVersionOnAdd(t *testing.T) {
	// addServer with input array → proxy injects version: 1 into each entry that omits it.
	srv, bodies := captureRequests(t, `{"data":{"addServer":{"server":[{"orbId":"x"}]}}}`)
	h := NewGraphQL(srv.URL, nil, slog.Default(), false)

	c, _ := newGQLCtx(t, map[string]any{
		"query": `mutation { addServer(input: $input) { server { orbId } } }`,
		"variables": map[string]any{
			"input": []any{
				map[string]any{"orbId": "x", "namespace": "ns"},
				map[string]any{"orbId": "y", "namespace": "ns", "version": 42}, // explicit, must not overwrite
			},
		},
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var fwd gqlRequest
	_ = json.Unmarshal((*bodies)[len(*bodies)-1], &fwd)
	input, _ := fwd.Variables["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected 2 input entries, got %d", len(input))
	}
	first, _ := input[0].(map[string]any)
	second, _ := input[1].(map[string]any)
	if v, _ := first["version"].(float64); int(v) != 1 {
		t.Errorf("expected first entry version=1, got %v", first["version"])
	}
	if v, _ := second["version"].(float64); int(v) != 42 {
		t.Errorf("expected second entry version=42 (caller-set, not overwritten), got %v", second["version"])
	}
}

func TestHandle_AutoIncrementInjectsIntoAnyArrayVariable(t *testing.T) {
	// Regression: the Edit Server modal passes idracSettings via a variable
	// named $idracInput, not $input. Before the fix, the proxy only looked at
	// the literal "input" key, so DGraph rejected addIdracSettings with
	// "variable.idracInput.0.version must be defined".
	srv, bodies := captureRequests(t, `{"data":{}}`)
	h := NewGraphQL(srv.URL, nil, slog.Default(), false)

	c, _ := newGQLCtx(t, map[string]any{
		"query": `mutation { addIdracSettings(input: $idracInput, upsert: true) { numUids } }`,
		"variables": map[string]any{
			"idracInput": []any{
				map[string]any{"orbId": "x-idrac", "sshEnabled": true},
			},
		},
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var fwd gqlRequest
	_ = json.Unmarshal((*bodies)[len(*bodies)-1], &fwd)
	arr, _ := fwd.Variables["idracInput"].([]any)
	if len(arr) != 1 {
		t.Fatalf("expected 1 idracInput entry, got %d", len(arr))
	}
	first, _ := arr[0].(map[string]any)
	if v, _ := first["version"].(float64); int(v) != 1 {
		t.Errorf("expected idracInput[0].version=1 injected, got %v", first["version"])
	}
}

func TestHandle_GQLErrorsSuppressAudit(t *testing.T) {
	// When DGraph returns errors, no audit event should be written.
	// With db=nil, any attempt to writeAuditEvent would panic — so if this test
	// passes without a nil-pointer panic, audit was correctly suppressed.
	srv := mockDGraph(t, `{"errors":[{"message":"something went wrong"}]}`)
	h := NewGraphQL(srv.URL, nil, slog.Default(), false)

	c, rec := newGQLCtx(t, map[string]any{
		"query": `mutation { addServer(input:[]) { server { id } } }`,
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "errors") {
		t.Errorf("expected errors in response, got: %s", rec.Body.String())
	}
}
