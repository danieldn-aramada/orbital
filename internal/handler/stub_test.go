package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newDGraphStub is a DGraph that answers every request with one canned body.
//
// It lived in datacenter_test.go until that page migrated to the generic
// renderer and the file was deleted; several suites still need it, so it moved
// here rather than being duplicated into each.
func newDGraphStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}
