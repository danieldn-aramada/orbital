package webrender

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// A template that emits output, THEN fails. This is the shape that matters:
// html/template streams, so by the time it evaluates .Missing it has already
// handed "BEFORE" to the writer. A template that fails on its first action
// would pass a naive implementation and prove nothing.
const partialFailTemplate = `<html><body>BEFORE{{.Missing}}AFTER</body></html>`

const goodTemplate = `<html><body>COMPLETE</body></html>`

type renderData struct{ Present string }

func newCtx() (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// TestRenderHTML_FailedRenderWritesNothing is the guarantee the helper exists
// for. Without buffering, the operator sees a 200 with a body that stops
// mid-page — no error status, nothing logged — and the symptom surfaces much
// later as "a button does nothing" (the real 2026-07-27 cluster-fragment bug).
func TestRenderHTML_FailedRenderWritesNothing(t *testing.T) {
	tmpl := template.Must(template.New("t").Parse(partialFailTemplate))
	c, rec := newCtx()

	err := RenderHTML(c, tmpl, "", renderData{Present: "x"})

	if err == nil {
		t.Fatal("expected an error from a template referencing a missing field")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected an empty body on failure, got %d bytes: %q", rec.Body.Len(), rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "BEFORE") {
		t.Error("partial output leaked to the response — the render was not buffered")
	}
	if rec.Code != http.StatusOK {
		// httptest defaults to 200 until something writes; the point is that
		// nothing was committed, so Echo's error handler can still set a 500.
		t.Logf("status %d (nothing committed, so the error handler can still act)", rec.Code)
	}
}

// TestRenderHTML_SuccessWritesCompleteBody is the negative of the above. A
// helper that satisfied the previous test by never writing anything would be
// useless; this pins that a good render still produces the whole document and
// the right content type.
func TestRenderHTML_SuccessWritesCompleteBody(t *testing.T) {
	tmpl := template.Must(template.New("t").Parse(goodTemplate))
	c, rec := newCtx()

	if err := RenderHTML(c, tmpl, "", renderData{Present: "x"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "COMPLETE") || !strings.Contains(body, "</html>") {
		t.Errorf("expected the complete document, got %q", body)
	}
	if ct := rec.Header().Get(echo.HeaderContentType); ct != "text/html; charset=utf-8" {
		t.Errorf("content type = %q, want text/html; charset=utf-8", ct)
	}
}

// TestRenderHTML_NamedBlock covers the ExecuteTemplate path (name != ""), which
// every HTMX fragment render uses.
func TestRenderHTML_NamedBlock(t *testing.T) {
	tmpl := template.Must(template.New("t").Parse(`{{define "frag"}}<div>FRAGMENT</div>{{end}}`))
	c, rec := newCtx()

	if err := RenderHTML(c, tmpl, "frag", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "FRAGMENT") {
		t.Errorf("expected the named block, got %q", rec.Body.String())
	}
}

// TestDirectExecuteTruncates is not a test of our code — it pins the stdlib
// behaviour the whole package is a response to. If this ever stops truncating,
// the buffering is no longer load-bearing and this package can be reconsidered.
// Until then it is the evidence that the rule is real rather than stylistic.
func TestDirectExecuteTruncates(t *testing.T) {
	tmpl := template.Must(template.New("t").Parse(partialFailTemplate))
	c, rec := newCtx()

	err := tmpl.Execute(c.Response(), renderData{Present: "x"})

	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(rec.Body.String(), "BEFORE") {
		t.Fatal("stdlib no longer streams partial output before erroring — re-evaluate this package's premise")
	}
	if strings.Contains(rec.Body.String(), "</html>") {
		t.Error("expected a TRUNCATED body")
	}
}
