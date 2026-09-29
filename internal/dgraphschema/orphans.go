package dgraphschema

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Orphan is a type the live schema still declares, the shipped schema no longer
// does, and which still has nodes behind it.
type Orphan struct {
	Type  string
	Count int
}

// Orphans reports nodes in the graph whose type the deployed schema no longer
// declares.
//
// This is the reverse of Drift, and Drift's rationale for not checking it was
// wrong. It holds for a FIELD — an unselected predicate lingers harmlessly —
// and fails for a TYPE. A node keeps its `dgraph.type` when its type leaves the
// schema, so `queryConfigItem` still matches it through the ConfigItem
// interface while every field on it, `__typename` included, resolves to
// nothing. The result is a row nothing can render: one leftover node took out
// the entire inventory page, with a DataTables alert naming only its own
// documentation.
//
// The equivalent in a relational store is dropping a table and leaving its rows
// behind, and it is reported as the integrity violation it is.
//
// ⚠️ Driven by the DATA, not by comparing two schema texts.
//
// An earlier version compared the shipped schema against the live one and found
// nothing, because the orphaning type is typically absent from BOTH — removing
// it from `schema.graphql` and applying that file is exactly how the nodes get
// stranded. The live schema says what is declared; only the graph says what
// exists. So: ask DGraph for every ConfigItem node whose type is none of the
// declared ones. The residue is precisely the orphans.
//
// COUNTS, not just names. A type removed after its last node was deleted is a
// normal, correct migration and must report nothing; only rows left behind are
// a problem. Reporting the bare name would cry wolf on every clean removal.
//
// Never deletes. Orbital surfaces divergence and does not auto-resolve it, and
// "make the schema apply succeed by removing data" is the exact shape of a
// silent loss.
func Orphans(ctx context.Context, adminURL, live string) ([]Orphan, error) {
	declared := declaredTypes(live)
	if len(declared) == 0 {
		// No schema, or one we could not parse. Every node would look orphaned,
		// which is a false alarm of the worst kind — say nothing instead.
		return nil, nil
	}
	counts, err := undeclaredNodeTypes(ctx, queryURLFor(adminURL), declared)
	if err != nil {
		return nil, err
	}
	out := make([]Orphan, 0, len(counts))
	for name, n := range counts {
		out = append(out, Orphan{Type: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// undeclaredNodeTypes counts ConfigItem nodes by type, for types the schema
// does not declare.
//
// One query: every ConfigItem node that is NOT any declared type. The filter is
// the complement of the schema, so a type nobody thought to look for is still
// caught — which is the point, since the orphaning type is by definition one
// that is no longer written down anywhere.
func undeclaredNodeTypes(ctx context.Context, queryURL string, declared []string) (map[string]int, error) {
	terms := make([]string, 0, len(declared))
	for _, t := range declared {
		terms = append(terms, "NOT type("+t+")")
	}
	q := "{ q(func: has(ConfigItem.orbId), first: 100000) @filter(" +
		strings.Join(terms, " AND ") + ") { uid dgraph.type } }"

	raw, err := postDQL(ctx, queryURL, q)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Q []struct {
				Types []string `json:"dgraph.type"`
			} `json:"q"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("dgraph query: %s", out.Errors[0].Message)
	}
	counts := map[string]int{}
	for _, row := range out.Data.Q {
		for _, t := range row.Types {
			// ConfigItem is on every one of these nodes by construction; the
			// interesting half is the concrete type that is no longer declared.
			if t == "ConfigItem" {
				continue
			}
			counts[t]++
		}
	}
	return counts, nil
}

// typeDecl matches a top-level type or interface declaration. Only `type` is
// collected: an interface holds no nodes of its own, so it can never be
// orphaned — its implementations are what carry data.
var typeDecl = regexp.MustCompile(`(?m)^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\b`)

// declaredTypes lists the concrete types the schema declares.
//
// Only `type` is collected: an interface holds no nodes of its own, so nothing
// can be orphaned under one — its implementations carry the data.
func declaredTypes(sdl string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range typeDecl.FindAllStringSubmatch(stripComments(sdl), -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

func stripComments(sdl string) string {
	var b strings.Builder
	for _, line := range strings.Split(sdl, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// queryURLFor turns an admin endpoint into the DQL query endpoint on the same
// alpha. Counting needs DQL: a type absent from the GraphQL schema has no
// generated query, which is precisely the situation being detected.
func queryURLFor(adminURL string) string {
	return strings.TrimSuffix(adminURL, "/admin") + "/query"
}

// postDQL runs one DQL query and returns the raw response body.
func postDQL(ctx context.Context, queryURL, q string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, queryURL, bytes.NewReader([]byte(q)))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/dql")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("dgraph query: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("dgraph query returned %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
}

// Describe renders orphans as one operator-readable line.
func Describe(orphans []Orphan) string {
	parts := make([]string, 0, len(orphans))
	for _, o := range orphans {
		parts = append(parts, fmt.Sprintf("%s (%d node%s)", o.Type, o.Count, plural(o.Count)))
	}
	return strings.Join(parts, ", ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
