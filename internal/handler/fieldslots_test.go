package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/configitems"
)

// TestServerTabFieldSlotsMatchRegistry pins the correspondence between the
// proposed-change mark slots in server-tab.gohtml and the editable fields in
// internal/configitems.Types.
//
// Every `tr[data-field]` inside a `data-field-orbid` table is a slot where a
// proposed-change mark can appear. A slot for a field the editor cannot write
// is dead UI — the mark could never fire — and a missing slot means a real
// proposal annotates nothing, which is the whole point of the mark (the banner
// only says "something is proposed"; the mark says WHICH FIELD).
//
// This invariant used to be a hardcoded `expect(slots).toBe(6)` at the end of
// e2e/proposed-change-marks.spec.ts. That assertion broke the moment two
// legitimate fields were added — `serialNumber` (2026-09-22) and `uHeight` —
// even though registry and template had been updated together correctly. A
// magic number cannot tell "someone added a field properly" from "someone added
// a slot with no field behind it", so it reported a false failure and told
// nobody anything. Comparing the two sets directly distinguishes them, needs no
// browser, and updates itself.
func TestServerTabFieldSlotsMatchRegistry(t *testing.T) {
	t.Chdir("../..")

	raw, err := os.ReadFile("web/templates/shared/partials/server-tab.gohtml")
	if err != nil {
		t.Fatalf("read server-tab.gohtml: %v", err)
	}

	// Which registry type owns each field table, keyed by the orbId expression
	// the template renders into data-field-orbid.
	owners := map[string]string{
		"{{.OrbID}}":            "Server",
		"{{.IdracOrbID}}":       "IdracSettings",
		"{{.MaintenanceOrbID}}": "ServerMaintenance",
	}

	blocks := fieldTableBlocks(t, string(raw))
	if len(blocks) != len(owners) {
		t.Fatalf("found %d data-field-orbid tables, want %d — a table was added or removed; update `owners`", len(blocks), len(owners))
	}

	for orbIDExpr, typeName := range owners {
		block, ok := blocks[orbIDExpr]
		if !ok {
			t.Errorf("no data-field-orbid=%q table found in server-tab.gohtml", orbIDExpr)
			continue
		}
		got := dataFieldNames(block)
		want := formFieldsFor(t, typeName)
		if !equalSets(got, want) {
			t.Errorf(
				"%s: mark slots in server-tab.gohtml do not match FormFields in configitems/registry.go\n"+
					"  template slots: %v\n"+
					"  registry fields: %v\n"+
					"  only in template (dead slot — a mark here can never fire): %v\n"+
					"  only in registry (editable field with nowhere to show a proposal): %v",
				typeName, got, want, difference(got, want), difference(want, got),
			)
		}
	}
}

var (
	fieldTableRe = regexp.MustCompile(`data-field-orbid="(\{\{\.[A-Za-z]+\}\})"`)
	dataFieldRe  = regexp.MustCompile(`<tr data-field="([A-Za-z0-9_]+)"`)
)

// fieldTableBlocks splits the template into one chunk per data-field-orbid
// table, each running to the closing </table>.
func fieldTableBlocks(t *testing.T, src string) map[string]string {
	t.Helper()
	locs := fieldTableRe.FindAllStringSubmatchIndex(src, -1)
	out := make(map[string]string, len(locs))
	for _, loc := range locs {
		expr := src[loc[2]:loc[3]]
		rest := src[loc[1]:]
		end := strings.Index(rest, "</table>")
		if end < 0 {
			t.Fatalf("data-field-orbid=%q table has no closing </table>", expr)
		}
		out[expr] = rest[:end]
	}
	return out
}

func dataFieldNames(block string) []string {
	var out []string
	for _, m := range dataFieldRe.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func formFieldsFor(t *testing.T, name string) []string {
	t.Helper()
	for _, ci := range configitems.Types {
		if ci.Name == name {
			out := append([]string(nil), ci.FormFields...)
			sort.Strings(out)
			return out
		}
	}
	t.Fatalf("no %q entry in configitems.Types", name)
	return nil
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func difference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}

// TestServerSummaryValuesMatchRegistry closes the THIRD list.
//
// TestServerTabFieldSlotsMatchRegistry compares the template's mark slots to
// configitems FormFields. There is a third hand-maintained list with the same
// obligation: the SummaryValuesJSON map in server.go, which supplies the CURRENT
// value each slot is compared against. A field present in the template and the
// registry but missing here has no current value to compare, so orbital.js
// (renderFieldMarks) cannot tell "proposed value already matches" from
// "proposed value differs".
//
// That was live at HEAD on 2026-09-23: `serialNumber` was absent from the map —
// directly beneath a comment reading "Exactly the Server FormFields in
// configitems/registry.go". The comment asserted the property; nothing checked
// it. Consequence: a redundant proposal on serialNumber rendered a mark it
// should not, and a proposal clearing it rendered none at all.
func TestServerSummaryValuesMatchRegistry(t *testing.T) {
	t.Chdir("../..")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "internal/handler/server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}

	var keys []string
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		id, ok := kv.Key.(*ast.Ident)
		if !ok || id.Name != "SummaryValuesJSON" {
			return true
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, el := range lit.Elts {
			if e, ok := el.(*ast.KeyValueExpr); ok {
				if bl, ok := e.Key.(*ast.BasicLit); ok {
					keys = append(keys, strings.Trim(bl.Value, `"`))
				}
			}
		}
		return false
	})

	if len(keys) == 0 {
		t.Fatal("found no SummaryValuesJSON map in server.go — did it move or change shape?")
	}
	sort.Strings(keys)

	want := formFieldsFor(t, "Server")
	if !equalSets(keys, want) {
		t.Errorf(
			"SummaryValuesJSON in server.go does not match Server FormFields in configitems/registry.go\n"+
				"  map keys:        %v\n"+
				"  registry fields: %v\n"+
				"  only in map (a value for a field nothing can propose): %v\n"+
				"  only in registry (NO current value, so the mark cannot tell\n"+
				"    'already matches' from 'differs'): %v",
			keys, want, difference(keys, want), difference(want, keys),
		)
	}
}
