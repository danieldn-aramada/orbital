package handler

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// TestInlineSelectorReject pins the Spike 31 guard: single-entity UPDATE
// mutations must use variable orbId + set (the only shape orbital can stamp);
// inline literals are rejected. Reads, adds, and the kill switch are the
// must-NOT-fire cases — the dangerous direction is over-rejecting a valid query.
func TestInlineSelectorReject(t *testing.T) {
	dg := mockDGraph(t, `{"data":{}}`)

	t.Run("inline update is rejected with an instructive envelope", func(t *testing.T) {
		h := NewGraphQL(dg.URL, nil, slog.Default(), true)
		c, rec := newGQLCtx(t, map[string]any{
			"query": `mutation { updateServer(input:{filter:{orbId:{eq:"alaska:SRV001"}}, set:{model:"x"}}) { server { id } } }`,
		})
		if err := h.Handle(c); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("inline update should be 400, got %d: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"code":"VARIABLE_FORM_REQUIRED"`) {
			t.Errorf("expected VARIABLE_FORM_REQUIRED code, got: %s", body)
		}
		if !strings.Contains(body, `"hint"`) {
			t.Errorf("rejection must carry a hint showing the fix, got: %s", body)
		}
	})

	t.Run("variable-form update passes the guard", func(t *testing.T) {
		h := NewGraphQL(dg.URL, nil, slog.Default(), true)
		c, rec := newGQLCtx(t, map[string]any{
			"query":         `mutation UpdateServer($orbId:String!,$set:ServerPatch!){ updateServer(input:{filter:{orbId:{eq:$orbId}},set:$set}){ server { id } } }`,
			"operationName": "UpdateServer",
			"variables":     map[string]any{"orbId": "alaska:SRV001", "set": map[string]any{"model": "x"}},
		})
		if err := h.Handle(c); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if strings.Contains(rec.Body.String(), "VARIABLE_FORM_REQUIRED") {
			t.Errorf("variable form must NOT be rejected, got: %s", rec.Body.String())
		}
	})

	t.Run("kill switch off lets inline through", func(t *testing.T) {
		h := NewGraphQL(dg.URL, nil, slog.Default(), false)
		c, rec := newGQLCtx(t, map[string]any{
			"query": `mutation { updateServer(input:{filter:{orbId:{eq:"alaska:SRV001"}}, set:{model:"x"}}) { server { id } } }`,
		})
		if err := h.Handle(c); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if strings.Contains(rec.Body.String(), "VARIABLE_FORM_REQUIRED") {
			t.Errorf("kill switch off must not reject, got: %s", rec.Body.String())
		}
	})

	t.Run("read with inline filter is never rejected", func(t *testing.T) {
		h := NewGraphQL(dg.URL, nil, slog.Default(), true)
		c, rec := newGQLCtx(t, map[string]any{
			"query": `query { queryServer(filter:{orbId:{eq:"alaska:SRV001"}}) { id } }`,
		})
		if err := h.Handle(c); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("read with inline filter must pass, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}
