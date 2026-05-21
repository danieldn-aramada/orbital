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
		query string
		want  bool
	}{
		{"mutation { addServer(input:[]) { server { id } } }", true},
		{"mutation Foo { updateDataCenter(input:{}) { dataCenter { id } } }", true},
		{"  mutation\n{ deleteServer(filter:{}) { server { id } } }", true},
		{"{ queryDataCenter { id name } }", false},
		{"query { queryServer { id } }", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isMutation(tt.query); got != tt.want {
			t.Errorf("isMutation(%q) = %v, want %v", tt.query, got, tt.want)
		}
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
		name       string
		query      string
		wantOps    []string
		wantTypes  []string
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
	h := NewGraphQL(srv.URL, nil, slog.Default())

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
	h := NewGraphQL(srv.URL, nil, slog.Default())

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

func TestHandle_IfVersionStrippedBeforeProxy(t *testing.T) {
	srv, bodies := captureRequests(t, `{"data":{}}`)
	h := NewGraphQL(srv.URL, nil, slog.Default())

	c, _ := newGQLCtx(t, map[string]any{
		"query":         `mutation { addServer(input:[]) { server { id } } }`,
		"variables":     map[string]any{"ifVersion": 5},
		"operationName": "",
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	lastBody := (*bodies)[len(*bodies)-1]
	var forwarded gqlRequest
	if err := json.Unmarshal(lastBody, &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded body: %v", err)
	}
	if _, ok := forwarded.Variables["ifVersion"]; ok {
		t.Error("ifVersion should be stripped before forwarding to DGraph")
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

	h := NewGraphQL(srv.URL, nil, slog.Default())

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "BeforeFetch") {
			w.Write([]byte(`{"data":{"getServer":{"id":"1","version":5}}}`)) //nolint:errcheck
		} else {
			w.Write([]byte(`{"data":{"updateServer":{"server":{"orbId":"alaska:SRV001"}}}}`)) //nolint:errcheck
		}
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default())

	c, rec := newGQLCtx(t, map[string]any{
		"query":         `mutation UpdateServer { updateServer(input:{}) { server { orbId } } }`,
		"operationName": "UpdateServer",
		"variables": map[string]any{
			"id":        "1",
			"ifVersion": 5,
		},
	})

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code == http.StatusConflict {
		t.Error("expected no conflict when versions match")
	}
	if !strings.Contains(rec.Body.String(), "alaska:SRV001") {
		t.Errorf("expected mutation response, got: %s", rec.Body.String())
	}
}

func TestHandle_GQLErrorsSuppressAudit(t *testing.T) {
	// When DGraph returns errors, no audit event should be written.
	// With db=nil, any attempt to writeAuditEvent would panic — so if this test
	// passes without a nil-pointer panic, audit was correctly suppressed.
	srv := mockDGraph(t, `{"errors":[{"message":"something went wrong"}]}`)
	h := NewGraphQL(srv.URL, nil, slog.Default())

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
