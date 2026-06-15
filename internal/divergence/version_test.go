package divergence_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/internal/divergence"
)

// Regression class: FetchCurrentVersion is the MVCC anchor. If it silently
// returns a stale or wrong value, the Accept handler's race-detection check
// against entry.IntendedAtVersion is meaningless. These cases pin each branch
// of the nil-vs-value contract documented at the function level.

func TestFetchCurrentVersion_ReturnsVersionFromGetGetter(t *testing.T) {
	srv := newDGraphStub(t, `{"data":{"getServer":{"version":7}}}`)
	defer srv.Close()

	v, err := divergence.FetchCurrentVersion(context.Background(), srv.URL, "Server", "alaska:SRV-1")
	if err != nil {
		t.Fatalf("FetchCurrentVersion: %v", err)
	}
	if v == nil || *v != 7 {
		t.Errorf("got %v, want pointer to 7", v)
	}
}

func TestFetchCurrentVersion_NilTypeOrOrbReturnsNil(t *testing.T) {
	srv := newDGraphStub(t, `{"data":{}}`)
	defer srv.Close()

	for _, c := range []struct{ typeName, orbID string }{
		{"", "alaska:SRV-1"},
		{"Server", ""},
	} {
		v, err := divergence.FetchCurrentVersion(context.Background(), srv.URL, c.typeName, c.orbID)
		if err != nil {
			t.Errorf("type=%q orbID=%q: unexpected error %v", c.typeName, c.orbID, err)
		}
		if v != nil {
			t.Errorf("type=%q orbID=%q: want nil, got %v", c.typeName, c.orbID, v)
		}
	}
}

func TestFetchCurrentVersion_EntityNotFoundReturnsNil(t *testing.T) {
	// DGraph returns the getter key with explicit null when the entity doesn't exist.
	srv := newDGraphStub(t, `{"data":{"getServer":null}}`)
	defer srv.Close()

	v, err := divergence.FetchCurrentVersion(context.Background(), srv.URL, "Server", "alaska:SRV-MISSING")
	if err != nil {
		t.Errorf("missing entity should not error: %v", err)
	}
	if v != nil {
		t.Errorf("missing entity should yield nil version, got %v", v)
	}
}

func TestFetchCurrentVersion_VersionFieldNullReturnsNil(t *testing.T) {
	// The entity exists but has no version (legacy data).
	srv := newDGraphStub(t, `{"data":{"getServer":{"version":null}}}`)
	defer srv.Close()

	v, err := divergence.FetchCurrentVersion(context.Background(), srv.URL, "Server", "alaska:SRV-1")
	if err != nil {
		t.Errorf("null version should not error: %v", err)
	}
	if v != nil {
		t.Errorf("null version field should yield nil, got %v", v)
	}
}

func TestFetchCurrentVersion_GraphQLErrorIsReported(t *testing.T) {
	// e.g. unknown TypeName produces a GraphQL error rather than 4xx HTTP.
	srv := newDGraphStub(t, `{"errors":[{"message":"Cannot query field 'getBogus' on type 'Query'"}]}`)
	defer srv.Close()

	v, err := divergence.FetchCurrentVersion(context.Background(), srv.URL, "Bogus", "x:y")
	if err == nil {
		t.Fatalf("unknown type should error, got nil")
	}
	if v != nil {
		t.Errorf("error path should not return a version, got %v", v)
	}
}

func newDGraphStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}
