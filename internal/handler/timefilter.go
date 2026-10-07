package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/labstack/echo/v4"
)

// TimeFields maps the timestamp fields a list returns, by their JSON name, to
// the columns they are read from. The JSON name is the filter's name:
// `createdAt` in the response is filtered as `createdAt_gte`, whatever the
// column is called. See docs/reference/REST-API.md.
type TimeFields map[string]string

var timeOps = []struct {
	suffix string
	pred   func(col string, arg any) *sql.Predicate
}{
	{"_gte", sql.GTE}, {"_gt", sql.GT}, {"_lte", sql.LTE}, {"_lt", sql.LT},
}

// QueryError is a malformed or unsupported query parameter. It renders as
// 400 BAD_USER_INPUT through WriteQueryError.
type QueryError struct{ Msg, Hint string }

func (e *QueryError) Error() string { return e.Msg }

// TimeFilters turns every `<field>_{gt,gte,lt,lte}` query parameter into a
// predicate on that field's column. Several are AND-ed, so a lower and an upper
// bound select a window.
//
// A parameter carrying an operator suffix for a field this list does not
// filter is REFUSED, not ignored: `createAt_gte` (a typo) would otherwise
// return the whole list, which reads exactly like a correct answer.
func TimeFilters[P ~func(*sql.Selector)](q url.Values, fields TimeFields) ([]P, error) {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var out []P
	for _, key := range keys {
		field, pred, ok := splitTimeOp(key)
		if !ok {
			continue
		}
		col, known := fields[field]
		if !known {
			return nil, &QueryError{
				Msg:  fmt.Sprintf("%q is not a time filter on this endpoint", key),
				Hint: timeFieldsHint(fields),
			}
		}
		for _, raw := range q[key] {
			if raw = strings.TrimSpace(raw); raw == "" {
				// An empty bound is no bound, as an empty value is for every
				// other filter — a template with no previous export renders one.
				continue
			}
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				hint := "Use an RFC3339 time, e.g. 2026-10-01T00:00:00Z."
				if strings.Contains(raw, " ") {
					// A literal `+` in a query string decodes to a space, so an
					// offset like +05:30 arrives broken unless it is encoded.
					hint += " Encode a + offset as %2B."
				}
				return nil, &QueryError{Msg: fmt.Sprintf("%s must be an RFC3339 time, got %q", key, raw), Hint: hint}
			}
			out = append(out, P(func(s *sql.Selector) { s.Where(pred(s.C(col), t)) }))
		}
	}
	return out, nil
}

func splitTimeOp(key string) (field string, pred func(string, any) *sql.Predicate, ok bool) {
	for _, op := range timeOps {
		if f, found := strings.CutSuffix(key, op.suffix); found && f != "" {
			return f, op.pred, true
		}
	}
	return "", nil, false
}

func timeFieldsHint(fields TimeFields) string {
	if len(fields) == 0 {
		return "This endpoint has no time filters."
	}
	names := make([]string, 0, len(fields))
	for f := range fields {
		names = append(names, f)
	}
	slices.Sort(names)
	return fmt.Sprintf("Time filters here: %s — each takes _gt, _gte, _lt or _lte with an RFC3339 time.",
		strings.Join(names, ", "))
}

// WriteQueryError renders a *QueryError as 400 BAD_USER_INPUT and returns any
// other error unchanged, for the caller to return.
func WriteQueryError(c echo.Context, err error) error {
	var qe *QueryError
	if errors.As(err, &qe) {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, qe.Msg, qe.Hint)
	}
	return err
}

// queryParam reads the first non-empty value of name, falling back to each
// deprecated alias in turn. New names win when a caller sends both.
func queryParam(c echo.Context, name string, deprecated ...string) string {
	for _, n := range append([]string{name}, deprecated...) {
		if v := strings.TrimSpace(c.QueryParam(n)); v != "" {
			return v
		}
	}
	return ""
}

// queryValues reads a repeatable parameter and its deprecated aliases as one
// de-duplicated list, empties dropped.
func queryValues(c echo.Context, name string, deprecated ...string) []string {
	var out []string
	for _, n := range append([]string{name}, deprecated...) {
		for _, v := range c.QueryParams()[n] {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}
