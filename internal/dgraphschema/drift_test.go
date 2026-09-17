package dgraphschema

import (
	"reflect"
	"strings"
	"testing"
)

const shippedSDL = `# Orbital DGraph Schema
interface ConfigItem {
    orbId: String! @id(interface: true)
    version: Int!
}

enum DataCenterModel {
    Triton
    Beacon
}

type DataCenter implements ConfigItem {
    model: DataCenterModel
}
`

func TestDrift_IdenticalSchemasReportNothing(t *testing.T) {
	if got := Drift(shippedSDL, shippedSDL); len(got) != 0 {
		t.Fatalf("identical schemas reported drift: %v", got)
	}
}

func TestDrift_ReportsDeclarationsMissingFromLive(t *testing.T) {
	live := strings.Replace(shippedSDL, "    Beacon\n", "", 1)
	got := Drift(shippedSDL, live)
	want := []string{"Beacon"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDrift_IgnoresCommentsAndBlankLines(t *testing.T) {
	// Same declarations, different comments/spacing/blank lines. DGraph stores
	// the SDL verbatim, so a reformat must not read as a schema change.
	live := "interface ConfigItem {\n" +
		"\torbId: String! @id(interface: true)   # the identity\n" +
		"\n\n" +
		"\tversion: Int!\n}\n" +
		"enum DataCenterModel { \n Triton\n Beacon\n}\n" +
		"# a trailing comment\n" +
		"type DataCenter implements ConfigItem {\n  model: DataCenterModel\n}\n"
	if got := Drift(shippedSDL, live); len(got) != 0 {
		t.Fatalf("comments/whitespace reported as drift: %v", got)
	}
}

func TestDrift_ExtraDeclarationsInLiveAreNotDrift(t *testing.T) {
	// DGraph schema application is additive at the RDF layer, so a predicate the
	// shipped file no longer declares lingers harmlessly. Reporting it would
	// train operators to ignore the warning.
	live := shippedSDL + "\ntype Legacy implements ConfigItem {\n    retired: String\n}\n"
	if got := Drift(shippedSDL, live); len(got) != 0 {
		t.Fatalf("extra live declarations reported as drift: %v", got)
	}
}

func TestDrift_EmptyLiveSchemaReportsEverything(t *testing.T) {
	got := Drift(shippedSDL, "")
	if len(got) == 0 {
		t.Fatal("empty live schema reported no drift")
	}
	for _, punct := range []string{"{", "}"} {
		for _, g := range got {
			if g == punct {
				t.Errorf("punctuation-only line %q reported as a missing declaration", g)
			}
		}
	}
}
