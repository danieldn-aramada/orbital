package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Compare-and-swap: the version predicate goes INSIDE the write.
//
// Before this, `version` was compared in Go against a snapshot fetched by a
// separate HTTP request, then the mutation was written with a filter on `orbId`
// alone — two DGraph transactions, so a writer committing in between was
// silently overwritten. Measured at 30 concurrent writers on one entity: 183 of
// 360 writes lost, both audited as ordinary updates while `version` advanced
// once. The reason it was worth fixing despite a 3–8 ms window is that the
// failure leaves no trace.
//
// These are the unit-level items (2, 4, 5, 6, 12). The concurrency items (1, 3)
// need real DGraph and live in the integration suite.

const casUpdateQuery = `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { server { orbId } } }`

// forwarded runs one mutation through Handle against a stub DGraph and returns
// the body that reached it, plus the recorder.
func forwarded(t *testing.T, dgraphResp string, vars map[string]any, query string) (*gqlRequest, *httptest.ResponseRecorder) {
	t.Helper()
	var last []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "BeforeFetch") {
			w.Write([]byte(`{"data":{"queryServer":[{"id":"1","orbId":"ns:server-A","version":7}]}}`)) //nolint:errcheck
			return
		}
		last = b
		w.Write([]byte(dgraphResp)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)
	c, rec := newGQLCtx(t, map[string]any{
		"query": query, "operationName": "UpdateServer", "variables": vars,
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var fwd gqlRequest
	if last != nil {
		_ = json.Unmarshal(last, &fwd)
	}
	return &fwd, rec
}

// ── 2. numUids: 0 on a guarded update is a 409, not a 200 ──────────────────

// The whole point. DGraph answers a filter that matched nothing with a
// SUCCESSFUL response — no `errors` array, `numUids: 0` — so without this the
// lost update is reported to the caller as success.
func TestCAS_NoRowMatchedIsAConflictNotSuccess(t *testing.T) {
	_, rec := forwarded(t, `{"data":{"updateServer":{"numUids":0}}}`,
		map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "version": 7},
		`mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a write that matched nothing was reported as success: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), CodeMVCCConflict) {
		t.Errorf("body does not carry %s: %s", CodeMVCCConflict, rec.Body.String())
	}
}

// The editor selects the payload rather than numUids, so a miss looks like an
// empty array instead. Both shapes have to be understood or the guard covers
// only the caller that happened to be tested.
func TestCAS_EmptyPayloadArrayIsAlsoAConflict(t *testing.T) {
	_, rec := forwarded(t, `{"data":{"updateServer":{"server":[]}}}`,
		map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "version": 7},
		casUpdateQuery)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for an empty payload array: %s", rec.Code, rec.Body.String())
	}
}

// ── 6. the forwarded body declares every variable it references ────────────

func TestCAS_PredicateIsInjectedAndItsVariableDeclared(t *testing.T) {
	fwd, rec := forwarded(t, `{"data":{"updateServer":{"server":[{"orbId":"ns:server-A"}]}}}`,
		map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "version": 7},
		casUpdateQuery)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(fwd.Query, "version: { eq: $version }") {
		t.Errorf("no version predicate in the forwarded filter — the write is still unguarded: %s", fwd.Query)
	}
	// Declared, or DGraph rejects the whole mutation for an undefined variable.
	if !strings.Contains(fwd.Query, "$version: Int!") {
		t.Errorf("$version is referenced but not declared: %s", fwd.Query)
	}
	// And the value must survive the strip that used to remove it as an
	// orbital-only variable.
	v, ok := fwd.Variables["version"]
	if !ok {
		t.Fatalf("version was stripped from the variables it is now declared with: %v", fwd.Variables)
	}
	if n, _ := toFloat64(v); int(n) != 7 {
		t.Errorf("version = %v, want 7", v)
	}
	// The counter clients must not write still is not in `set`.
	set, _ := fwd.Variables["set"].(map[string]any)
	if _, has := set["version"]; !has {
		t.Error("set.version missing — stamping stopped happening")
	}
}

// ── 4. the negative: an unguarded update is untouched ──────────────────────

// A client that did not ask for a check must be byte-for-byte unaffected, and —
// the part that is easy to get wrong — a legitimate zero-row result must stay
// 200. Turning that into a 409 would invent a conflict where no concurrency
// happened at all.
func TestCAS_UnguardedUpdateIsUnchangedAndZeroRowsStays200(t *testing.T) {
	fwd, rec := forwarded(t, `{"data":{"updateServer":{"numUids":0}}}`,
		map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}},
		`mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an unguarded update was turned into a conflict: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(fwd.Query, "version") {
		t.Errorf("a predicate was injected into an unguarded mutation: %s", fwd.Query)
	}
}

// ── 5. fail closed: no predicate, no write ─────────────────────────────────

// The property that makes rewriting the query text acceptable at all. A shape
// the rewriter does not recognise must be REFUSED, never sent unguarded —
// otherwise a regex miss reproduces exactly the silent bypass being fixed.
func TestCAS_UnrecognisedShapeIsRefusedNotSentUnguarded(t *testing.T) {
	cases := []struct {
		name, query, why string
	}{
		{
			name:  "inline orbId literal, no $orbId to filter on",
			query: `mutation UpdateServer($set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: "ns:server-A" } }, set: $set }) { numUids } }`,
			why:   "No `filter:",
		},
		{
			name:  "two filters, so guarding one leaves the other unguarded",
			query: `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { a: updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } b: updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`,
			why:   "more than one orbId filter",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(string(b), "BeforeFetch") {
					w.Write([]byte(`{"data":{"queryServer":[{"id":"1","version":7}]}}`)) //nolint:errcheck
					return
				}
				reached = true
				w.Write([]byte(`{"data":{"updateServer":{"numUids":1}}}`)) //nolint:errcheck
			}))
			t.Cleanup(srv.Close)

			h := NewGraphQL(srv.URL, nil, slog.Default(), false)
			c, rec := newGQLCtx(t, map[string]any{
				"query": tc.query, "operationName": "UpdateServer",
				"variables": map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "version": 7},
			})
			if err := h.Handle(c); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if reached {
				t.Fatal("the mutation was sent to DGraph without the precondition the caller asked for")
			}
			if rec.Code < 400 {
				t.Errorf("status = %d, want a refusal: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.why) {
				t.Errorf("refusal does not say why (%q): %s", tc.why, rec.Body.String())
			}
		})
	}
}

// A malformed token is validated at injection too. checkVersion returns early
// when no current state resolved, so without this a garbage `version` could
// reach the rewriter unchecked.
func TestCAS_MalformedIfVersionIsRefusedAtInjection(t *testing.T) {
	req := &gqlRequest{Query: casUpdateQuery, Variables: map[string]any{"version": "not-a-number"}}
	perr := injectVersionPredicate(req)
	if perr == nil {
		t.Fatal("a malformed version was accepted by the injector")
	}
	if perr.Status != http.StatusBadRequest || perr.Code != CodeBadUserInput {
		t.Errorf("status=%d code=%s, want 400 %s", perr.Status, perr.Code, CodeBadUserInput)
	}
}

// ── the shapes merge builds itself ─────────────────────────────────────────

// The design argument rests on "orbital mandates exactly one filter shape",
// which is true of CLIENTS. Merge writes its own queries: an update whose
// variable list grows a `$remove`, and a delete whose `filter:` sits directly on
// the field rather than inside `input:`. The filter target is identical in all
// three, which is why the premise holds — but it holds for a reason the original
// argument did not state, so it is asserted rather than assumed.
func TestCAS_InjectsIntoTheQueriesMergeBuilds(t *testing.T) {
	cases := map[string]string{
		"update with remove": `mutation UpdateServer($orbId: String!, $set: ServerPatch!, $remove: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set, remove: $remove}) { numUids } }`,
		"delete":             `mutation DeleteServer($orbId: String!) { deleteServer(filter: {orbId: {eq: $orbId}}) { numUids } }`,
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			req := &gqlRequest{Query: q, Variables: map[string]any{"orbId": "ns:server-A", "version": 3}}
			if perr := injectVersionPredicate(req); perr != nil {
				t.Fatalf("refused a query orbital itself builds: %v", perr.Message)
			}
			if !strings.Contains(req.Query, "version: { eq: $version }") {
				t.Errorf("no predicate injected: %s", req.Query)
			}
			if !strings.Contains(req.Query, "$version: Int!") {
				t.Errorf("$version not declared: %s", req.Query)
			}
		})
	}
}

// ── 12. a refused write records no audit event ─────────────────────────────

// Structural — Handle's audit call sits behind the early error return — but the
// plan says verify rather than assume, because the refactor moved that return.
// db is nil here, so any attempt to audit panics the goroutine.
func TestCAS_ConflictWritesNoAuditEvent(t *testing.T) {
	_, rec := forwarded(t, `{"data":{"updateServer":{"numUids":0}}}`,
		map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "version": 7},
		`mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	// Reaching here without a panic is the assertion.
}

// ── 8. a transient abort is retried, and a persistent one is DISTINCT ──────

// The version predicate makes aborts more likely: two writers filtering the same
// predicate contend where an unfiltered write would not. Without a bounded
// retry this trades a rare silent bug for a common loud one.
func TestCAS_TransientTransactionAbortIsRetried(t *testing.T) {
	var mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "BeforeFetch") {
			w.Write([]byte(`{"data":{"queryServer":[{"id":"1","version":7}]}}`)) //nolint:errcheck
			return
		}
		mutations++
		if mutations == 1 {
			w.Write([]byte(`{"errors":[{"message":"Transaction has been aborted. Please retry"}]}`)) //nolint:errcheck
			return
		}
		w.Write([]byte(`{"data":{"updateServer":{"numUids":1}}}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)
	c, rec := newGQLCtx(t, map[string]any{
		"query":         casUpdateQuery,
		"operationName": "UpdateServer",
		"variables":     map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "version": 7},
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if mutations < 2 {
		t.Fatalf("mutation attempts = %d — a retryable abort was not retried", mutations)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — the retry succeeded but the caller was told otherwise: %s", rec.Code, rec.Body.String())
	}
}

// And when it does not clear: a DISTINCT code. MVCC_CONFLICT would tell the
// caller their data is stale and to re-read; here the same request is still
// valid and the store was simply busy — different remedy, different code.
func TestCAS_PersistentAbortIsNotReportedAsAnMVCCConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "BeforeFetch") {
			w.Write([]byte(`{"data":{"queryServer":[{"id":"1","version":7}]}}`)) //nolint:errcheck
			return
		}
		w.Write([]byte(`{"errors":[{"message":"Transaction has been aborted. Please retry"}]}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)
	c, rec := newGQLCtx(t, map[string]any{
		"query":         casUpdateQuery,
		"operationName": "UpdateServer",
		"variables":     map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "version": 7},
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), CodeMVCCConflict) {
		t.Errorf("contention reported as a stale-data conflict: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), CodeWriteContention) {
		t.Errorf("body does not carry %s: %s", CodeWriteContention, rec.Body.String())
	}
}

// ── the "silently ignored" hole, and whether CAS closed it ─────────────────

// Filed as debt when the write pre-flight moved: `checkVersion` returns early
// when no current state resolved, so a supplied `version` was dropped and the
// write proceeded UNGUARDED with a 200. A caller that asked for a check and did
// not get one is worse off than one that never asked.
//
// The predicate closes it without a decision being needed: injection does not
// depend on the pre-flight read, so a token that cannot be COMPARED in Go is
// still ENFORCED by DGraph. These pin that, because "it follows from the
// design" is exactly the claim that turns out to be wrong later.
func TestCAS_TokenIsStillEnforcedWhenThePreFlightCannotResolveTheEntity(t *testing.T) {
	// Before-fetch comes back empty, so checkVersion sees current == nil and
	// returns without comparing anything.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "BeforeFetch") {
			w.Write([]byte(`{"data":{"queryServer":[]}}`)) //nolint:errcheck
			return
		}
		// The filtered mutation matches nothing, which is what DGraph answers
		// when the version has moved or the row is gone.
		w.Write([]byte(`{"data":{"updateServer":{"numUids":0}}}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)
	c, rec := newGQLCtx(t, map[string]any{
		"query":         casUpdateQuery,
		"operationName": "UpdateServer",
		"variables":     map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "version": 7},
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("an unguardable version was dropped and the write reported success: %s", rec.Body.String())
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

// The other half of the old hole: a mutation touching more than one type never
// resolved a single entity, so the token was dropped there too. It is now
// refused — one filter cannot guard two targets.
func TestCAS_MultiTypeMutationCarryingIfVersionIsRefusedNotDropped(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "BeforeFetch") {
			w.Write([]byte(`{"data":{"queryServer":[]}}`)) //nolint:errcheck
			return
		}
		reached = true
		w.Write([]byte(`{"data":{}}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)
	c, rec := newGQLCtx(t, map[string]any{
		"query": `mutation Compound($orbId: String!, $set: ServerPatch!, $rset: RackPatch!) {
			updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids }
			updateRack(input: { filter: { orbId: { eq: $orbId } }, set: $rset }) { numUids }
		}`,
		"operationName": "Compound",
		"variables": map[string]any{
			"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"},
			"rset": map[string]any{"name": "r"}, "version": 7,
		},
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if reached {
		t.Fatal("a multi-target mutation carrying version was sent with the token dropped")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// ── the pre-rename spelling is refused, not ignored ────────────────────────

// `ifVersion` was renamed to `version` on 2026-09-04. GraphQL ignores unknown
// variables, so without this a client that has not caught up writes UNGUARDED
// and gets a 200 — losing the precondition it asked for with nothing to tell it.
// That is the failure this whole area exists to remove, so the old name is
// refused by name.
func TestCAS_PreRenameIfVersionIsRefusedNotIgnored(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "BeforeFetch") {
			w.Write([]byte(`{"data":{"queryServer":[{"id":"1","version":7}]}}`)) //nolint:errcheck
			return
		}
		reached = true
		w.Write([]byte(`{"data":{"updateServer":{"numUids":1}}}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	h := NewGraphQL(srv.URL, nil, slog.Default(), false)
	c, rec := newGQLCtx(t, map[string]any{
		"query":         casUpdateQuery,
		"operationName": "UpdateServer",
		"variables":     map[string]any{"orbId": "ns:server-A", "set": map[string]any{"hostname": "x"}, "ifVersion": 7},
	})
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if reached {
		t.Fatal("a mutation carrying the old `ifVersion` was sent unguarded")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "renamed to `version`") {
		t.Errorf("refusal does not name the replacement: %s", rec.Body.String())
	}
}
