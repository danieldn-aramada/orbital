// Package configitems resolves what orbital's ConfigItem pages show and edit,
// from two artifacts that are deliberately separate:
//
//	schema/schema.graphql   what EXISTS — fields, relationships, identity
//	config/views.yaml       what a PAGE does with it
//
// # What used to be here
//
// A hand-maintained Go registry: per-type editable field lists, before-fetch
// selections, payload fields, interface lists, the set of type names, and
// containment — which edges are a page's edit unit and which parent a
// multi-parent node calls home. All of it has left. Field metadata and the type
// set are read from the DEPLOYED schema; containment is declared in the views
// config, because every consumer of it was a view (what the editor groups into
// one tree, what the audit tab rolls up, what a reviewer is deemed to have
// looked at) and none of it ever reached the data.
//
// Nothing in this package is per-type any more.
package configitems

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The views configuration: what one ConfigItem's page shows and edits.
//
// # Why this is a file and not the schema
//
// Orbital used to carry thirteen docstring annotations in schema.graphql, of
// which eleven were view configuration. DGraph rejects unknown directives, so
// that configuration was being smuggled through description strings — a channel
// designed for documentation. `menuWeight:` is the proof: a menu position is not
// a property of a graph type by any reading, and it lived there because the
// schema was the only channel available.
//
// Splitting them gives each its own cadence. A schema change is a version bump,
// an apply to DGraph and reindex care; a views change is an edit to this file,
// picked up with no schema operation at all.
//
// Two annotations stay in the schema deliberately — `jsonString` (what a String
// CONTAINS) and `orbIdSuffix:` (identity). Both are facts about the data, not
// about a page. Identity especially must never depend on a display decision:
// flipping `editable` must not re-key a node.
//
// # Two layers, both files, no database
//
//	config/views.yaml shipped in the image      ← the default
//	          ↓ merged, overlay wins PER VIEW
//	ConfigMap overlay, PARTIAL                  ← only what this deployment changes
//
// The overlay being PARTIAL is the whole point. A deployment that reorders one
// page's columns declares that one page and still receives every view
// improvement shipped later; a deployment that copies the whole file is frozen
// at the shape it copied, forever, with no signal.

// ViewConfig is the whole views document — the shipped default, an overlay, or
// the two merged.
//
// TWO NOUNS. A PAGE is a URL, a menu entry and a detail layout; there are four,
// and the top level of Pages is 1:1 with the menu. A TYPE is how something
// renders WHEREVER it renders; there are twenty, because sixteen of them appear
// only as rows on a page belonging to something else — StorageDevice's
// `wwn: label: WWN` is a column header on the SERVER page.
//
// They are separate sections rather than nested because four types render on
// several pages each (IPAddress on four, NetworkInterface on three), and nesting
// would write their display config once per page — thirteen duplicate blocks
// that nothing can check against each other. That is the drift this whole layer
// exists to delete.
type ViewConfig struct {
	// SchemaVersion is the `schema/VERSION` label this file was authored
	// against. A COARSE signal only: that file is bumped by hand and only for
	// DGraph-relevant changes, so two different schemas can both be `v14`.
	// Per-member validation against live introspection is what actually catches
	// a stale path.
	SchemaVersion string `yaml:"schemaVersion,omitempty"`

	// Pages are the renderable pages, keyed by root type. 1:1 with the menu.
	//
	// A type ABSENT from this map has no URL and no menu entry, and a row of
	// that type does not link. That is a legitimate statement — "this is data
	// that belongs on someone else's page" — not an oversight.
	Pages map[string]PageDecl `yaml:"pages,omitempty"`

	// Types is how a type renders wherever it renders. INHERITS along the
	// schema's interfaces, so an implementation declares only what it changes.
	Types map[string]TypeDecl `yaml:"types,omitempty"`

	// Containment names the ordered candidate owners of a type whose ownership
	// the SCHEMA cannot express.
	//
	// Containment is otherwise DERIVED: a child whose back-edge to a parent is
	// non-null cannot outlive it. One type defeats that — NetworkInterface is
	// owned by exactly one of {server, networkDevice, networkAdapter}, an XOR
	// that no single edge can declare non-null.
	//
	// It carries TWO jobs: what dies with the parent, and which edge a
	// first-time create links through. The second used to come from
	// `derivesIdFrom:`, which is single-valued and therefore wrong for a type
	// with three candidate parents.
	//
	// Ordered, most-specific first; the first edge a node actually has is its
	// owner. Never a map — Go map iteration is randomised, and this decides a
	// delete.
	Containment map[string][]string `yaml:"containment,omitempty"`

	// CanonicalParent answers the INVERSE question to a page: a page is indexed
	// by root ("what does the Server page show?"), this is indexed by child
	// ("this IPAddress turned up in a diff — whose page is its home?").
	//
	// It matters MORE now that pageless types have no URL: without it there is
	// no answer at all for an IPAddress.
	CanonicalParent map[string][]CanonicalParentDecl `yaml:"canonicalParent,omitempty"`
}

// PageDecl is one page: a URL, a place in the menu, and a detail layout.
type PageDecl struct {
	// Slug is the URL segment, and the nav label derives from it. A CONTRACT:
	// integrators and bookmarks depend on it. Empty means the kebab-cased plural
	// of the type name.
	Slug string `yaml:"slug,omitempty"`

	// MenuWeight orders the menu. ORDER ONLY — membership is membership of
	// Pages. Conflating the two reversed a settled decision once already.
	MenuWeight int `yaml:"menuWeight,omitempty"`

	// FilterBy is the single column the LIST page offers as a filter dropdown.
	// One of the few genuinely per-screen facts; `columns:` and `order:` are
	// not, and live on the type.
	FilterBy string `yaml:"filterBy,omitempty"`

	// Summary is the left panel: scalars, then reference rows.
	Summary SummaryDecl `yaml:"summary,omitempty"`

	// Tabs is the tab strip, in the order it renders. The audit tab is always
	// last and is never declared.
	Tabs []MemberDecl `yaml:"tabs,omitempty"`
}

// SummaryDecl is the left panel of a detail page.
//
// The asymmetry is deliberate. A SCALAR is cheap to show and expensive to miss,
// so a field added to the schema appears on its own and is removed by naming it.
// A RELATIONSHIP is a row you chose, so it is listed.
//
// ⚠️ A single relationship added to the schema will NOT appear until someone
// lists it here. That will surprise somebody; it is the price of not having a
// layout change arrive uninvited.
type SummaryDecl struct {
	// IgnoreFields drops scalars. Subtractive.
	IgnoreFields []string `yaml:"ignoreFields,omitempty"`

	// Refs are the SINGLE relationships rendered as link rows, in row order.
	// A list here is refused: a list cannot be one row.
	//
	// Listing them is what retired the `viewIgnored` annotation — a relationship
	// you did not want shown previously had no way out other than a flag.
	Refs []string `yaml:"refs,omitempty"`
}

// TypeDecl is how a type renders, wherever it renders.
type TypeDecl struct {
	// Order pins the leading display fields; the rest follow alphabetically.
	// A PREFIX, never a complete list — a complete list is a frozen view in
	// which a field added later silently never appears.
	Order []string `yaml:"order,omitempty"`

	// Columns are extra columns whose value lives at the end of a path.
	//
	// On the TYPE, not the page, and Rack is the proof: it declares
	// `servers.count` and Rack is not a page. A Server's computed columns apply
	// on /servers AND inside a rack's servers tab AND a network device's.
	Columns []string `yaml:"columns,omitempty"`

	// RefColumns are the relationship columns a row of this type carries —
	// `Data Center`, `Rack`, `OOB IP` on a server.
	//
	// LISTED, not derived, because a relationship is a relationship: the file's
	// own rule is that scalars are subtractive and relationships are declared.
	// It WAS derived — every single-cardinality edge to a ConfigItem became a
	// column — and that put `Idrac Settings` and `Server Configuration Profile`
	// on the servers list, where no hand-written page had ever shown them, and
	// meant any edge added to the schema silently widened every table of that
	// type. Fixed 2026-10-05.
	//
	// On the TYPE for the same reason Columns is: a Server row keeps its columns
	// inside a data centre's tab and a network device's, and declaring that per
	// page would be the same list three times. A column pointing back at the
	// entity whose page you are on is dropped automatically.
	RefColumns []string `yaml:"refColumns,omitempty"`

	// Fields configures this type's own scalars.
	Fields map[string]FieldDecl `yaml:"fields,omitempty"`
}

// MemberDecl is one relationship a page includes, as a tab.
type MemberDecl struct {
	// Path is a field name, or a two-segment dotted path whose rows are the far
	// type (`storageControllers.storageDevices`). A path exists because
	// StorageController has no scalar fields of its own, so a controllers tab is
	// a list of bare names and the content is the devices.
	Path string `yaml:"path"`

	// Editable means THE EDITOR WRITES THIS, and nothing else.
	//
	// It used to also carry containment — what dies with the parent and what the
	// audit tab rolls up — which put `editable: true` on lists the editor has
	// never been able to edit. Containment is derived from the schema now.
	//
	// Kept rather than derived because it expresses something underivable: a
	// rack page shows its servers and may choose not to let you edit them from
	// there. No page wants that today, so the flag is currently redundant with
	// "a contained single that has editable fields".
	Editable bool `yaml:"editable,omitempty"`
}

// UnmarshalYAML accepts either form:
//
//   - networkAdapters                              # the common case
//   - { path: idracSettings, editable: true }
//
// The short form is not a convenience: `editable` is true on three tabs out of
// eighteen, and requiring a mapping everywhere would bury the exception in
// noise. A reader scanning a tab list is reading PATHS; the mapping form is what
// says "something unusual here".
func (m *MemberDecl) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&m.Path)
	}
	type plain MemberDecl // no recursion
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*m = MemberDecl(p)
	return nil
}

// FieldDecl configures one scalar field.
type FieldDecl struct {
	// Label overrides the title-cased field name. Declare only what
	// title-casing gets WRONG — acronyms, essentially (`cni` → "Cni",
	// `oobIP` → "Oob IP").
	Label string `yaml:"label,omitempty"`

	// DetailOnly keeps a field off every table — a list page and a relationship
	// table are the same problem. Placement is DECLARED, never inferred from
	// what the field holds.
	DetailOnly bool `yaml:"detailOnly,omitempty"`

	// Editable decides whether the EDITOR offers the field. Nil means the
	// default for its origin: true for a type's own scalars, false for the
	// ConfigItem interface's, which are identity and provenance.
	//
	// EDITOR-scoped, not API-scoped: a scanner must still be able to write
	// capacityBytes through the API.
	Editable *bool `yaml:"editable,omitempty"`
}

// CanonicalParentDecl is one candidate home for a multi-parent type.
type CanonicalParentDecl struct {
	// Type is the parent, Field the edge on the CHILD pointing up, Down the edge
	// on the PARENT pointing back. Down may be empty where no downward edge
	// exists.
	Type  string `yaml:"type"`
	Field string `yaml:"field"`
	Down  string `yaml:"down,omitempty"`
}

// ErrNoViewConfig reports that the shipped default could not be read. It is
// distinguished from a parse failure because the two need different answers: a
// missing file is a packaging fault, a bad one is an authoring fault.
var ErrNoViewConfig = errors.New("views configuration not found")

// LoadViewConfig reads the shipped default and merges a partial overlay onto it.
//
// The returned hash covers BOTH files' bytes. It is what the resolver watches to
// re-derive without a restart, and what the editor stamps into its payload so a
// save opened under an older view can be refused rather than silently emitting a
// `remove` for an entity the view no longer carries.
//
// A missing or unreadable OVERLAY is a warning, never an error: the deployment
// renders the shipped defaults, which is the correct degraded state. A missing
// DEFAULT is an error — there is nothing to degrade to.
func LoadViewConfig(defaultPath, overlayPath string) (cfg ViewConfig, hash string, warnings []string, err error) {
	base, err := os.ReadFile(defaultPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ViewConfig{}, "", nil, fmt.Errorf("%w at %s", ErrNoViewConfig, defaultPath)
		}
		return ViewConfig{}, "", nil, fmt.Errorf("read views config %s: %w", defaultPath, err)
	}
	if err := yaml.Unmarshal(base, &cfg); err != nil {
		return ViewConfig{}, "", nil, fmt.Errorf("parse views config %s: %w", defaultPath, err)
	}

	sum := sha256.New()
	sum.Write(base)

	if overlayPath != "" {
		raw, readErr := os.ReadFile(overlayPath)
		switch {
		case errors.Is(readErr, fs.ErrNotExist):
			// Normal in every deployment that customises nothing. The overlay
			// path is configured once, in the manifest, and the ConfigMap it
			// points at is optional.
			warnings = append(warnings, "no views overlay at "+overlayPath+"; serving the shipped defaults")
		case readErr != nil:
			warnings = append(warnings, "cannot read views overlay "+overlayPath+": "+readErr.Error()+"; serving the shipped defaults")
		default:
			var overlay ViewConfig
			if parseErr := yaml.Unmarshal(raw, &overlay); parseErr != nil {
				// NOT fatal, and NOT silent. A malformed overlay must not take
				// orbital's pages down, and must not pass unremarked either —
				// otherwise an operator edits a ConfigMap, sees no change, and
				// has nothing to read.
				warnings = append(warnings, "views overlay "+overlayPath+" is not valid YAML ("+parseErr.Error()+"); serving the shipped defaults")
			} else {
				cfg = MergeViewConfig(cfg, overlay)
				sum.Write(raw)
			}
		}
	}
	return cfg, hex.EncodeToString(sum.Sum(nil)), warnings, nil
}

// MergeViewConfig lays an overlay over a base, replacing PER VIEW.
//
// Per view, not per key: a declared view is a coherent statement about one page,
// and merging members or fields key-by-key would make "remove this member" —
// which is the single most likely customisation — inexpressible without a
// tombstone syntax. A deployment that declares `Server` owns the Server page and
// still receives every change shipped to the other eighteen.
//
// The same reasoning applies one level up to interfaceFields and canonicalParent,
// which merge per field and per type respectively.
func MergeViewConfig(base, overlay ViewConfig) ViewConfig {
	out := ViewConfig{
		SchemaVersion:   base.SchemaVersion,
		Pages:           map[string]PageDecl{},
		Types:           map[string]TypeDecl{},
		Containment:     map[string][]string{},
		CanonicalParent: map[string][]CanonicalParentDecl{},
	}
	if overlay.SchemaVersion != "" {
		out.SchemaVersion = overlay.SchemaVersion
	}
	for k, v := range base.Pages {
		out.Pages[k] = v
	}
	for k, v := range overlay.Pages {
		out.Pages[k] = v
	}
	for k, v := range base.Types {
		out.Types[k] = v
	}
	for k, v := range overlay.Types {
		out.Types[k] = v
	}
	for k, v := range base.Containment {
		out.Containment[k] = v
	}
	for k, v := range overlay.Containment {
		out.Containment[k] = v
	}
	for k, v := range base.CanonicalParent {
		out.CanonicalParent[k] = v
	}
	for k, v := range overlay.CanonicalParent {
		out.CanonicalParent[k] = v
	}
	return out
}

// Validate drops every declaration the DEPLOYED schema cannot support and says
// what it dropped.
//
// This is the half that annotations got for free and a separate file loses.
// An annotation and the schema it described were the same artifact, deployed
// together, unable to disagree; a file and a graph can be different versions
// with nothing saying so.
//
// NEVER FATAL — a stale view must not stop a page rendering. NEVER SILENT — a
// declaration that reads as correct and does nothing is worse than one nobody
// wrote. Both halves are load-bearing; neither is negotiable.
//
// `deployedVersion` is the schema version orbital records for the running graph.
// Pass "" to skip the comparison.
func (c ViewConfig) Validate(types map[string]TypeInfo, deployedVersion string) (ViewConfig, []string) {
	var warn []string

	if c.SchemaVersion != "" && deployedVersion != "" && c.SchemaVersion != deployedVersion {
		warn = append(warn, "views config declares schemaVersion "+c.SchemaVersion+
			" but the deployed schema is "+deployedVersion+"; check the declarations against it")
	}

	out := ViewConfig{
		SchemaVersion:   c.SchemaVersion,
		Pages:           map[string]PageDecl{},
		Types:           map[string]TypeDecl{},
		Containment:     map[string][]string{},
		CanonicalParent: map[string][]CanonicalParentDecl{},
	}
	// An INTERFACE is a legitimate key here even though introspection returns
	// only the types that implement it: `ConfigItem` is where `orbId: label: Orb
	// ID` lives, and DGraph forbids an implementor from redeclaring it, so there
	// is nowhere else it could go.
	known := make(map[string]bool, len(types)*2)
	for name, info := range types {
		known[name] = true
		for _, in := range info.Implements {
			known[in] = true
		}
	}
	for _, k := range sortedKeys(c.Types) {
		v := c.Types[k]
		if !known[k] {
			warn = append(warn, k+": no such type or interface in the deployed schema; its display config is ignored")
			continue
		}
		// A ref column is ONE CELL, so the same three rules the summary refs
		// obey apply: the field must exist, must not be a scalar, and must not
		// be a list. A dropped one is said out loud — a column that silently is
		// not there reads as "this type has no such relationship".
		//
		// Checked against the concrete type only: an interface key (ConfigItem)
		// carries labels, never ref columns, and introspection does not report
		// an interface's fields here.
		if info, concrete := types[k]; concrete {
			kept := v.RefColumns[:0:0]
			for _, r := range v.RefColumns {
				f, found := fieldNamed(info, r)
				switch {
				case !found:
					warn = append(warn, k+": refColumn "+r+" is not a field on "+k)
				case f.Kind == "SCALAR" || f.Kind == "ENUM":
					warn = append(warn, k+": refColumn "+r+" is a scalar; scalars are columns already")
				case f.IsList:
					warn = append(warn, k+": refColumn "+r+" is a LIST, which cannot be one cell — make it a tab")
				default:
					if _, isConfigItem := types[f.TypeName]; !isConfigItem {
						warn = append(warn, k+": refColumn "+r+" points at "+f.TypeName+", which is not a ConfigItem")
						continue
					}
					kept = append(kept, r)
				}
			}
			v.RefColumns = kept
		}
		out.Types[k] = v
	}

	for _, typeName := range sortedKeys(c.Pages) {
		decl := c.Pages[typeName]
		info, known := types[typeName]
		if !known {
			warn = append(warn, typeName+": no such type in the deployed schema; its page is ignored")
			continue
		}
		kept := PageDecl{
			Slug:       decl.Slug,
			MenuWeight: decl.MenuWeight,
			FilterBy:   decl.FilterBy,
			Summary:    SummaryDecl{IgnoreFields: decl.Summary.IgnoreFields},
		}
		// A ref is ONE ROW. A list cannot be one row, and accepting it would
		// render a link to whichever element DGraph returned first.
		for _, r := range decl.Summary.Refs {
			f, found := fieldNamed(info, r)
			switch {
			case !found:
				warn = append(warn, typeName+": ref "+r+" is not a field on "+typeName)
			case f.Kind == "SCALAR" || f.Kind == "ENUM":
				warn = append(warn, typeName+": ref "+r+" is a scalar; scalars are rows already")
			case f.IsList:
				warn = append(warn, typeName+": ref "+r+" is a LIST, which cannot be a summary row — make it a tab")
			default:
				if _, isConfigItem := types[f.TypeName]; !isConfigItem {
					warn = append(warn, typeName+": ref "+r+" points at "+f.TypeName+", which is not a ConfigItem")
					continue
				}
				kept.Summary.Refs = append(kept.Summary.Refs, r)
			}
		}
		for _, m := range decl.Tabs {
			if reason := validateMemberPath(types, info, typeName, m.Path); reason != "" {
				warn = append(warn, typeName+": tab "+m.Path+" — "+reason)
				continue
			}
			// `editable:` on a LIST is refused and SAID — the tab still renders,
			// it just cannot be written from here. An edit target is addressed
			// by path and a path cannot say which row of a list it means, so the
			// editor has never been able to honour this. Dropping the flag in
			// silence is the worse failure: the page looks configured for an
			// edit that will never arrive, and nothing on the page says so.
			if m.Editable && memberIsList(types, info, m.Path) {
				warn = append(warn, typeName+": tab "+m.Path+" is a LIST and cannot be editable — "+
					"an edit target is addressed by path, and a path cannot name one row; "+
					"the tab renders read-only")
				m.Editable = false
			}
			kept.Tabs = append(kept.Tabs, m)
		}
		out.Pages[typeName] = kept
	}

	for _, childType := range sortedKeys(c.Containment) {
		info, known := types[childType]
		if !known {
			warn = append(warn, childType+": no such type in the deployed schema; its containment is ignored")
			continue
		}
		var kept []string
		for _, edge := range c.Containment[childType] {
			f, found := fieldNamed(info, edge)
			if !found || f.Kind == "SCALAR" || f.Kind == "ENUM" {
				warn = append(warn, childType+": containment edge "+edge+" is not a relationship on "+childType)
				continue
			}
			kept = append(kept, edge)
		}
		if len(kept) > 0 {
			out.Containment[childType] = kept
		}
	}

	for _, childType := range sortedKeys(c.CanonicalParent) {
		info, known := types[childType]
		if !known {
			warn = append(warn, childType+": no such type in the deployed schema; its canonicalParent is ignored")
			continue
		}
		var kept []CanonicalParentDecl
		for _, p := range c.CanonicalParent[childType] {
			f, found := fieldNamed(info, p.Field)
			if !found {
				warn = append(warn, childType+": canonicalParent edge "+p.Field+" is not a field on "+childType)
				continue
			}
			if f.Kind == "SCALAR" || f.Kind == "ENUM" {
				warn = append(warn, childType+": canonicalParent edge "+p.Field+" is a scalar, not a relationship")
				continue
			}
			if p.Down != "" {
				parent, parentKnown := types[p.Type]
				if !parentKnown {
					warn = append(warn, childType+": canonicalParent type "+p.Type+" is not in the deployed schema")
					continue
				}
				if _, found := fieldNamed(parent, p.Down); !found {
					warn = append(warn, childType+": canonicalParent down-edge "+p.Type+"."+p.Down+" does not exist")
					continue
				}
			}
			kept = append(kept, p)
		}
		if len(kept) > 0 {
			out.CanonicalParent[childType] = kept
		}
	}

	return out, warn
}

// TypeOf returns a type's display config, INHERITED along the schema's
// interfaces.
//
// A declaration on the type itself wins field-by-field; anything it does not say
// comes from an interface it implements. EksaKubernetesCluster therefore
// declares nothing and renders with KubernetesCluster's order and labels —
// without this those six lines would be duplicated and could drift from the
// interface they came from.
func (c ViewConfig) TypeOf(info TypeInfo, typeName string) TypeDecl {
	own := c.Types[typeName]
	merged := TypeDecl{Order: own.Order, Columns: own.Columns, RefColumns: own.RefColumns, Fields: map[string]FieldDecl{}}
	// Interfaces first, so the type's own declaration overwrites them.
	for _, iface := range info.Implements {
		in := c.Types[iface]
		if len(merged.Order) == 0 {
			merged.Order = in.Order
		}
		if len(merged.Columns) == 0 {
			merged.Columns = in.Columns
		}
		// EksaKubernetesCluster declares none and inherits KubernetesCluster's,
		// the same as Order and Columns. Forgetting this list here is silent:
		// every declaration resolves to nothing and the columns just disappear.
		if len(merged.RefColumns) == 0 {
			merged.RefColumns = in.RefColumns
		}
		for f, d := range in.Fields {
			merged.Fields[f] = d
		}
	}
	for f, d := range own.Fields {
		merged.Fields[f] = d
	}
	return merged
}

// BackEdge is one child→parent relationship the schema declares as non-null.
//
// Read from the SDL rather than introspection, because introspection reports a
// field's type but orbital's DerivedField flattens the NonNull wrapper away —
// and nullability is exactly the fact this rule turns on.
type BackEdge struct {
	Child  string // the type that cannot survive
	Field  string // the field on the child pointing up
	Parent string // the type it points at
}

// NonNullBackEdges parses an SDL for every non-null relationship field.
func NonNullBackEdges(sdl string) []BackEdge {
	scalars := map[string]bool{
		"ID": true, "String": true, "Int": true, "Int64": true,
		"Float": true, "Boolean": true, "DateTime": true,
	}
	var out []BackEdge
	cur := ""
	for _, ln := range strings.Split(sdl, "\n") {
		if m := declRe.FindStringSubmatch(ln); m != nil {
			cur = m[1]
			continue
		}
		if cur == "" {
			continue
		}
		if m := nonNullFieldRe.FindStringSubmatch(ln); m != nil && !scalars[m[2]] {
			out = append(out, BackEdge{Child: cur, Field: m[1], Parent: m[2]})
		}
	}
	return out
}

var (
	declRe         = regexp.MustCompile(`^(?:type|interface)\s+(\w+)`)
	nonNullFieldRe = regexp.MustCompile(`^\s+(\w+):\s*(\w+)!`)
)

// validateMemberPath returns why a member path cannot be supported, or "".
//
// A member walks LIST relationships and produces ROWS, so a list hop is the
// normal case and not an error — `storageControllers.storageDevices` is the
// reason paths exist at all. That is the opposite of a `columns:` path, which
// produces one CELL and therefore must stay on single relationships. The two
// limits guard different hazards and must not be harmonised.
func validateMemberPath(types map[string]TypeInfo, info TypeInfo, typeName, path string) string {
	if path == "" {
		return "empty path"
	}
	segs := strings.Split(path, ".")
	if len(segs) > 2 {
		return "a member path is at most two segments; deeper is a graph browser"
	}
	cur, curName := info, typeName
	for _, seg := range segs {
		f, found := fieldNamed(cur, seg)
		if !found {
			return curName + " has no field " + seg
		}
		if f.Kind == "SCALAR" || f.Kind == "ENUM" {
			return seg + " is a scalar, not a relationship"
		}
		next, known := types[f.TypeName]
		if !known {
			return f.TypeName + " is not a ConfigItem type, so it has no page"
		}
		cur, curName = next, f.TypeName
	}
	return ""
}

// memberIsList reports whether a validated member path ends on a list edge.
// Called only after validateMemberPath has accepted the path, so every segment
// resolves.
func memberIsList(types map[string]TypeInfo, info TypeInfo, path string) bool {
	cur := info
	var last DerivedField
	for _, seg := range strings.Split(path, ".") {
		f, found := fieldNamed(cur, seg)
		if !found {
			return false
		}
		last = f
		cur = types[f.TypeName]
	}
	return last.IsList
}

// CanonicalParentOf returns the first declared parent edge the node actually
// carries — its presentation home.
//
// `has` reports whether the child instance holds that edge. First match wins:
// precedence is list ORDER, which is exactly what could not be derived from
// membership, and exactly why this section exists rather than being inferred.
func (c ViewConfig) CanonicalParentOf(childType string, has func(field string) bool) (CanonicalParentDecl, bool) {
	for _, p := range c.CanonicalParent[childType] {
		if has(p.Field) {
			return p, true
		}
	}
	return CanonicalParentDecl{}, false
}

// TabsOf returns a page's tab strip. A type with no page declares none.
func (c ViewConfig) TabsOf(typeName string) []MemberDecl { return c.Pages[typeName].Tabs }

// RefsOf returns a page's summary link rows.
func (c ViewConfig) RefsOf(typeName string) []string { return c.Pages[typeName].Summary.Refs }

// IsPage reports whether a type has a page — a URL, a menu entry, and rows of
// that type linking to it.
func (c ViewConfig) IsPage(typeName string) bool {
	_, ok := c.Pages[typeName]
	return ok
}

// InNav reports a page's menu position. Membership is membership of Pages;
// MenuWeight is ORDER ONLY. Conflating the two reversed a settled decision once.
func (c ViewConfig) InNav(typeName string) (int, bool) {
	p, ok := c.Pages[typeName]
	return p.MenuWeight, ok
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
