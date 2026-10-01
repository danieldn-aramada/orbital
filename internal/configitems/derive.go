package configitems

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// SchemaClient reads the schema of a RUNNING DGraph. Two channels, because
// neither alone is sufficient: introspection exposes fields, kinds and
// docstrings but DROPS directives, so `@hasInverse` is only visible in the SDL.
type SchemaClient interface {
	// DeployedSDL returns the SDL as DGraph currently holds it (getGQLSchema).
	// Used for the cache-invalidation hash, not parsed in Stage 1.
	DeployedSDL(ctx context.Context) (string, error)
	// Introspect returns, per ConfigItem implementing type, its field names
	// paired with whether each is an editable scalar, plus the interface's own
	// field names.
	Introspect(ctx context.Context) (types map[string]TypeInfo, ifaceFields []string, err error)
}

// TypeInfo is one ConfigItem type as the running schema describes it.
type TypeInfo struct {
	// Doc is the type's docstring, which carries type-level annotations such
	// as `slug:`.
	Doc    string
	Fields []DerivedField

	// Implements lists the interfaces this type implements, straight from
	// introspection. Used to resolve interface-typed ownership: the backup
	// sub-kinds declare an owner of `KubernetesCluster` (the interface), and a
	// concrete EksaKubernetesCluster owns them because it implements it.
	Implements []string

	// OrbIDSuffix is the token an owned child's derived orbId ends with:
	// `<namespace>:<parentName>-<suffix>`. Defaults to the lower-cased type
	// name, so only the irregular ones need an annotation.
	OrbIDSuffix string

	// PayloadField is the field on `Add<Type>Payload` that returns the affected
	// rows, so the audit extractor can find the orbId in a mutation response.
	//
	// DGraph's convention is the lower-cased type name, but it does not always
	// pluralise or case cleanly — S3Sync's payload field is `s3Sync`. Reading it
	// from the schema gets the irregular cases right without anyone listing
	// them, which is exactly why this stopped being hand-maintained.
	PayloadField string

	// IsInterface marks a view backed by a GraphQL interface rather than a
	// concrete type. DGraph generates query<Interface> but NOT get<Interface>,
	// so such a view can back a LIST page and never a detail page — the detail
	// route resolves the concrete type and renders with that type's view.
	IsInterface bool

	// PossibleTypes are the concrete types implementing this interface. Empty
	// for a concrete type.
	PossibleTypes []string
}

// DerivedField is one field as the running schema describes it.
type DerivedField struct {
	Name string
	// Editable is true for a non-list SCALAR/ENUM. Edges, nested objects and
	// lists are not editable scalars.
	Editable bool
	// Doc is the field's docstring, surfaced by introspection as `description`.
	Doc string

	// Kind is the UNWRAPPED GraphQL kind: SCALAR, ENUM, OBJECT or INTERFACE.
	// A relationship is anything that is not SCALAR/ENUM, and relationships are
	// what a detail page renders as tabs.
	Kind string

	// TypeName is the unwrapped named type, e.g. "IdracSettings" — the other end
	// of a relationship.
	TypeName string

	// IsList distinguishes `[Server]` from `Server`: a list relationship renders
	// as a table, a single one as a panel.
	IsList bool
}

// EditableAnnotation re-admits ConfigItem interface fields for editing on one
// type.
//
//	"""editable: name"""
//	type DataCenter implements ConfigItem {
//
// `name` is declared on the ConfigItem interface, so "type fields minus
// interface fields" removes it from every type — but DataCenter and Rack have
// always exposed it for editing.
//
// TYPE-level, not field-level, and that is forced: DGraph forbids redeclaring
// an interface field on an implementor, so `DataCenter.name` cannot carry a
// docstring of its own. This was a hardcoded map in Go until 2026-09-29; the
// comment above it had promised an annotation since the day it was written.
const EditableAnnotation = "editable:"

// EditableInterfaceFields returns the interface fields a type re-admits.
func EditableInterfaceFields(typeDoc string) []string {
	v := annotationValue(typeDoc, EditableAnnotation)
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if f := strings.TrimSpace(part); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Derive computes each type's editable scalar set from the running schema:
// the object type's fields, minus the ConfigItem interface's fields, minus
// anything that is not a plain scalar — then re-admitting whatever the type's
// `editable:` annotation names.
func Derive(types map[string]TypeInfo, ifaceFields []string) map[string][]string {
	iface := make(map[string]bool, len(ifaceFields))
	for _, f := range ifaceFields {
		iface[f] = true
	}

	derived := make(map[string][]string, len(types))
	for typeName, info := range types {
		readmit := make(map[string]bool)
		for _, f := range EditableInterfaceFields(info.Doc) {
			readmit[f] = true
		}

		var scalars []string
		for _, f := range info.Fields {
			if !f.Editable {
				continue
			}
			if iface[f.Name] && !readmit[f.Name] {
				continue
			}
			if IsEditorIgnored(f.Doc) {
				continue
			}
			scalars = append(scalars, f.Name)
		}
		sort.Strings(scalars)
		derived[typeName] = scalars
	}
	return derived
}

// BeforeSelection generates the audit before-fetch selection for a type from
// the same derived model, so the two can never disagree. Shape:
//
//	id orbId name version <own scalars> <childField> { <child scalars> } …
//
// Hand-maintaining this beside the field list is a Hi-severity debt row: drop a
// field from one and the mutation still succeeds while the audit event carries
// no `changes` at all — no error, nothing in the log.
func BeforeSelection(typeName string, fields FieldsFor, children func(string) []Type) string {
	sel := "id orbId name version"
	for _, f := range fields(typeName) {
		if f == "name" {
			continue // already in the head
		}
		sel += " " + f
	}
	for _, ch := range children(typeName) {
		if ch.ChildField == "" {
			continue
		}
		childScalars := fields(ch.Name)
		if len(childScalars) == 0 {
			continue
		}
		sel += " " + ch.ChildField + " { " + joinFields(childScalars) + " }"
	}
	return sel
}

func joinFields(f []string) string {
	s := ""
	for i, v := range f {
		if i > 0 {
			s += " "
		}
		s += v
	}
	return s
}

// Resolver serves the derived field sets, continuously rather than as a
// boot-time snapshot.
//
// DGraph may come up AFTER orbital — normal on `make up`, and on an orb that
// has not imported yet. A resolve-once-at-boot design would leave orbital
// permanently degraded until someone restarted it, which is worse than the
// compiled-in fallback it replaces.
//
// Caching is the point: a page load must never trigger introspection. The
// derived set is held in memory and re-derived only when the DEPLOYED schema
// changes, detected by hashing the SDL. That check itself is rate-limited by
// checkEvery, so the steady state for any number of page loads is zero network
// calls.
//
// NOTE the invalidation signal is a hash of the DEPLOYED schema ALONE — not the
// file-vs-deployed comparison dgraphschema.StartCheck performs. Those answer
// different questions: a deployment whose graph exactly matches its file has no
// difference to report, yet its registry still needs deriving.
type Resolver struct {
	client     SchemaClient
	checkEvery time.Duration
	now        func() time.Time
	logger     *slog.Logger

	mu        sync.RWMutex
	derived   map[string][]string
	snapshot  map[string]TypeInfo
	views     []View
	viewsErr  error
	sdlHash   string
	lastCheck time.Time
	resolved  bool
}

func NewResolver(client SchemaClient, checkEvery time.Duration) *Resolver {
	return &Resolver{client: client, checkEvery: checkEvery, now: time.Now}
}

// Fields returns the editable scalar set for a type.
//
// It NEVER returns an empty list to mean "cannot see the schema" — an empty
// list reads as "this type has no fields", which is a different and misleading
// statement. When the schema is unreachable and nothing is cached it returns an
// error naming the reason, so the editor can say why instead of rendering an
// empty form.
func (r *Resolver) Fields(ctx context.Context, typeName string) ([]string, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.derived[typeName]
	if !ok {
		return nil, fmt.Errorf("type %q is not in the deployed schema", typeName)
	}
	return append([]string(nil), f...), nil
}

// TypeInfoFor returns a type's schema-derived metadata, served from the same
// cache as the field sets.
func (r *Resolver) TypeInfoFor(ctx context.Context, typeName string) (TypeInfo, error) {
	if err := r.ensure(ctx); err != nil {
		return TypeInfo{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot[typeName], nil
}

// Views returns the resolved view list — orbital's APIResourceList.
//
// Served from the same cache as the field sets, so rendering a page or listing
// the views costs no network work; both are re-derived only when the deployed
// schema's hash moves.
func (r *Resolver) Views(ctx context.Context) ([]View, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.viewsErr != nil {
		return nil, r.viewsErr
	}
	return append([]View(nil), r.views...), nil
}

// All returns every derived type's field set.
func (r *Resolver) All(ctx context.Context) (map[string][]string, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]string, len(r.derived))
	for k, v := range r.derived {
		out[k] = append([]string(nil), v...)
	}
	return out, nil
}

// ensure resolves if needed. Three paths: never resolved (resolve, and surface
// any failure); resolved recently (serve cache, no network); resolved but the
// check window elapsed (hash the deployed SDL, re-derive only if it moved).
func (r *Resolver) ensure(ctx context.Context) error {
	r.mu.RLock()
	resolved, last := r.resolved, r.lastCheck
	r.mu.RUnlock()

	if resolved && r.now().Sub(last) < r.checkEvery {
		return nil
	}

	sdl, err := r.client.DeployedSDL(ctx)
	if err != nil {
		if resolved {
			// Serve the last good answer rather than breaking the editor over a
			// transient blip; the next check retries.
			return nil
		}
		return fmt.Errorf("cannot read the schema from DGraph, so the editable fields are unknown: %w", err)
	}

	sum := sha256.Sum256([]byte(sdl))
	hash := hex.EncodeToString(sum[:])

	r.mu.RLock()
	unchanged := resolved && hash == r.sdlHash
	r.mu.RUnlock()
	if unchanged {
		r.mu.Lock()
		r.lastCheck = r.now()
		r.mu.Unlock()
		return nil
	}

	types, iface, err := r.client.Introspect(ctx)
	if err != nil {
		if resolved {
			return nil
		}
		return fmt.Errorf("cannot introspect the schema, so the editable fields are unknown: %w", err)
	}

	views, viewsErr := ResolveViews(types, iface)

	r.mu.Lock()
	r.derived = Derive(types, iface)
	r.snapshot = types
	r.views, r.viewsErr = views, viewsErr
	r.sdlHash = hash
	r.lastCheck = r.now()
	r.resolved = true
	r.mu.Unlock()

	// Logged on EVERY re-derive, not only the first: this covers boot and every
	// later schema change, and an environment whose annotations went missing
	// after a redeploy is exactly as invisible as one that never had them.
	if r.logger != nil {
		rep := Annotations(types)
		r.logger.Info("resolved schema annotations from the deployed schema",
			"editor_ignored_total", rep.EditorIgnoredTotal,
			"editor_ignored_by_type", rep.EditorIgnored,
			"types", len(types))
		for _, u := range rep.Unknown {
			r.logger.Warn("unrecognised orbital annotation in the deployed schema; the field is NOT suppressed",
				"where", u)
		}
		// A facet that named a field the page does not render is dropped
		// silently otherwise — the schema claims the list page has a filter
		// and it simply does not appear.
		for _, w := range FacetWarnings(types, views) {
			r.logger.Warn("facet annotation ignored; the list page has no filter dropdown for it",
				"where", w)
		}
	}
	return nil
}

// WithLogger attaches a logger so each re-derive reports what it resolved.
func (r *Resolver) WithLogger(l *slog.Logger) *Resolver {
	r.logger = l
	return r
}

// EditorIgnoredAnnotation opts a field OUT of the editor, from the schema.
//
// Editability is editable-UNLESS-annotated (decided 2026-09-24), so this is the
// only opt-out. It is EDITOR-scoped, not API-scoped: an ignored field is still
// writable through the API, because a scanner must still be able to write e.g.
// capacityBytes. It only means "do not offer this to a human in the editor".
//
// It rides a docstring rather than a directive because DGraph REJECTS unknown
// directives outright ("Undefined directive orbital"), while docstrings
// round-trip through getGQLSchema AND surface in introspection as `description`
// — both verified against a live instance 2026-09-24:
//
//	"""editorIgnored"""
//	capacityBytes: Int64
//
// Annotation-only changes do NOT bump schema/VERSION: no data contract moves.
const EditorIgnoredAnnotation = "editorIgnored"

// IsEditorIgnored reports whether a field's docstring opts it out of the editor.
//
// Matching is exact on a trimmed line so that ordinary prose mentioning the word
// does not silently disable a field. A docstring may carry human text on other
// lines.
func IsEditorIgnored(doc string) bool {
	return hasAnnotationLine(doc, EditorIgnoredAnnotation)
}

// knownAnnotations is orbital's whole annotation vocabulary.
//
// It exists so UnknownAnnotations can tell a TYPO from a word it has not been
// taught. Every annotation added anywhere must be listed here, or it reports
// itself as a typo — which is noisy, and worse, trains whoever reads the
// startup log to ignore the one report that matters.
var knownAnnotations = []string{
	EditorIgnoredAnnotation, // bare word
	JSONStringAnnotation,    // bare word
	SlugAnnotation,          // "slug:" prefix
	OrbIDSuffixAnnotation,   // "orbIdSuffix:" prefix
	OrderAnnotation,         // "order:" prefix
	IncludeAnnotation,       // "include:" prefix
	LabelAnnotation,         // "label:" prefix
	DetailOnlyAnnotation,    // bare word
	EditableAnnotation,      // "editable:" prefix
	FacetAnnotation,         // "facet:" prefix
}

// UnknownAnnotations returns docstring lines that LOOK like an orbital
// annotation but match none.
//
// An annotation typo is otherwise silent: """editorIgnroed""" is a perfectly
// valid docstring that simply never matches, so the field stays editable and
// nothing says so. Callers should log these at startup — a schema that lies
// about its own intent is the failure mode this design is most exposed to.
func UnknownAnnotations(doc string) []string {
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || matchesKnownAnnotation(t) {
			continue
		}
		// A single lowerCamel word, or a `word:` prefix form, reads as an
		// intended annotation rather than prose.
		head := t
		if i := strings.Index(t, ":"); i > 0 {
			head = t[:i+1]
		}
		if strings.Contains(head, " ") || len(head) <= 2 || strings.ToLower(head[:1]) != head[:1] {
			continue
		}
		out = append(out, t)
	}
	return out
}

func matchesKnownAnnotation(line string) bool {
	for _, a := range knownAnnotations {
		if line == a || (strings.HasSuffix(a, ":") && strings.HasPrefix(line, a)) {
			return true
		}
	}
	return false
}

// AnnotationReport summarises the orbital annotations found in the DEPLOYED
// schema, so a deployment can see what it actually resolved rather than what
// the source tree implies.
//
// ENVIRONMENT COUPLING is the hazard this exists for. Once editability is read
// from the running schema, it comes from the DEPLOYED schema and not from the
// binary — so an environment still carrying an older schema.graphql silently
// gets MORE editable fields, because the annotations suppressing them are not
// there. Nothing fails; the editor simply offers fields it should not, in one
// environment and not another. That is the "schema that lies about its own
// intent" failure at environment scope rather than typo scope, and the only
// cheap defence is making the resolved count visible: zero-when-expecting-29 is
// obvious, and invisible otherwise.
type AnnotationReport struct {
	// EditorIgnored counts fields suppressed by the annotation, per type.
	EditorIgnored map[string]int
	// EditorIgnoredTotal is the figure to eyeball against what the source tree
	// is expected to carry.
	EditorIgnoredTotal int
	// Unknown lists "Type.field: text" for docstring lines that look like an
	// intended annotation but match none — the silent-typo case.
	Unknown []string
}

// Annotations inspects introspected types and reports what was resolved.
func Annotations(types map[string]TypeInfo) AnnotationReport {
	rep := AnnotationReport{EditorIgnored: map[string]int{}}
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, typeName := range names {
		if types[typeName].IsInterface {
			// A sub-interface's annotations are INHERITED by each implementing
			// type and already counted there. Counting the interface too
			// double-counts every one of them, which turns this total into a
			// number nobody can reconcile against the schema file.
			continue
		}
		for _, f := range types[typeName].Fields {
			if f.Doc == "" {
				continue
			}
			if IsEditorIgnored(f.Doc) {
				rep.EditorIgnored[typeName]++
				rep.EditorIgnoredTotal++
			}
			for _, u := range UnknownAnnotations(f.Doc) {
				rep.Unknown = append(rep.Unknown, typeName+"."+f.Name+": "+u)
			}
		}
	}
	return rep
}

// JSONStringAnnotation marks a field declared `String` whose VALUE is JSON.
//
// The page handler parses such a field before handing it to the JSON editor, so
// it displays as nested structure; on submit the editor MUST stringify it again
// or DGraph rejects it with "cannot use as String". Nothing about the field's
// TYPE says this — it is String either way — which is why it cannot be derived
// and has to be declared.
//
//	"""jsonString"""
//	assetDataV2: String
const JSONStringAnnotation = "jsonString"

// IsJSONString reports whether a field's docstring marks it as JSON-in-a-String.
func IsJSONString(doc string) bool {
	return hasAnnotationLine(doc, JSONStringAnnotation)
}

// OrbIDSuffixAnnotation overrides the token an owned child's orbId ends with.
//
//	"""orbIdSuffix: idrac"""
//	type IdracSettings implements ConfigItem {
//
// The default is the lower-cased type name, so only the irregular ones are
// annotated — IdracSettings is `idrac`, not `idracsettings`. These were a Go
// switch statement; the convention belongs with the type it names.
const OrbIDSuffixAnnotation = "orbIdSuffix:"

// OrbIDSuffixFor returns a type's orbId suffix, preferring an annotation.
func OrbIDSuffixFor(typeName, typeDoc string) string {
	if v := annotationValue(typeDoc, OrbIDSuffixAnnotation); v != "" {
		return v
	}
	return strings.ToLower(typeName)
}

// hasAnnotationLine reports whether a docstring carries `name` as a whole
// trimmed line. Exact-match, so prose mentioning the word does not trigger it.
func hasAnnotationLine(doc, name string) bool {
	if doc == "" {
		return false
	}
	for _, line := range strings.Split(doc, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// annotationValue returns the value of a `prefix: value` annotation line, or "".
func annotationValue(doc, prefix string) string {
	for _, line := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, prefix) {
			continue
		}
		if v := strings.TrimSpace(strings.TrimPrefix(t, prefix)); v != "" {
			return v
		}
	}
	return ""
}

// JSONStringFieldsFor returns the JSON-in-a-String fields of a type.
func JSONStringFieldsFor(info TypeInfo) []string {
	var out []string
	for _, f := range info.Fields {
		if IsJSONString(f.Doc) {
			out = append(out, f.Name)
		}
	}
	return out
}

// OrderAnnotation pins the leading fields of a type's display order.
//
//	"""order: name, provider, clusterType"""
//	interface KubernetesCluster {
//
// Everything not named falls in alphabetically BEHIND the pinned list. That
// partial form is deliberate: a complete ordered list is a frozen view — a
// field added in a later release would have no place in it and would silently
// never appear. A prefix leaves the tail open, so new fields still arrive.
//
// This is the SHARED layer, and it lives in the schema rather than a database
// because a default belongs in version control: it ships with the build, it
// diffs, and it reverts. Per-deployment and per-user overrides layer on top of
// it later.
//
// A name that no longer exists is IGNORED, not an error. A pin is a preference
// about order, and a field being removed is not a reason to refuse to render a
// page.
const OrderAnnotation = "order:"

// OrderFor returns the pinned field order declared by a type, or nil.
func OrderFor(typeDoc string) []string {
	v := annotationValue(typeDoc, OrderAnnotation)
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if f := strings.TrimSpace(part); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ApplyOrder puts the pinned fields first, in the order pinned, and leaves the
// rest as they were — which is alphabetical, since displayScalars sorts.
//
// Pins naming a field this type does not have are skipped rather than
// reported here: an interface's pin list is inherited by every implementation,
// and an implementation legitimately lacks the fields its siblings add.
func ApplyOrder(fields, pinned []string) []string {
	if len(pinned) == 0 {
		return fields
	}
	have := make(map[string]bool, len(fields))
	for _, f := range fields {
		have[f] = true
	}
	out := make([]string, 0, len(fields))
	taken := make(map[string]bool, len(pinned))
	for _, p := range pinned {
		if have[p] && !taken[p] {
			taken[p] = true
			out = append(out, p)
		}
	}
	for _, f := range fields {
		if !taken[f] {
			out = append(out, f)
		}
	}
	return out
}

// FacetAnnotation names ONE column a list page offers as a filter dropdown.
//
//	"""facet: dataCenter"""
//	type Server implements ConfigItem {
//
// The bespoke Servers and Clusters pages each carried a hand-built "All Data
// Centers" select; both died with their templates and nobody noticed, because
// the JavaScript that built them survived and simply returns early now.
//
// Declared rather than derived. Cardinality alone identifies the right columns
// on today's data — 9 data centers across 190 servers, against 190 distinct
// service tags — but a threshold makes the CONTROL appear and disappear as the
// data moves, and a dropdown that vanished because someone seeded thirty more
// rows is not a UI anyone can explain. IPv4 sorting gets to be derived because
// its test is per-value and stable; "is this worth filtering by" is a judgement
// about the page, which is what an annotation is for.
//
// ONE field. The two pages that lost a dropdown had exactly one each, and
// widening to a list is a compatible change if a page ever wants two — where
// refusing extra fields now would not be.
const FacetAnnotation = "facet:"

// FacetFor returns the field a type declares as its list-page facet, plus any
// extra fields the annotation named.
//
// Extras are RETURNED rather than ignored so the caller can say so: only one is
// supported, and silently honouring the first would leave someone convinced
// their second dropdown was broken.
func FacetFor(typeDoc string) (string, []string) {
	v := annotationValue(typeDoc, FacetAnnotation)
	if v == "" {
		return "", nil
	}
	var fields []string
	for _, part := range strings.Split(v, ",") {
		if f := strings.TrimSpace(part); f != "" {
			fields = append(fields, f)
		}
	}
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], fields[1:]
}

// IncludeAnnotation adds a relationship the type does not hold directly, named
// by a PATH through one it does.
//
//	"""include: storageControllers.storageDevices"""
//	type Server implements ConfigItem {
//
// A server's disks hang off its storage controllers, and a controller has no
// scalars of its own — so the controller table is a list of bare names and the
// disks, which are the reason anyone opens the page, appear nowhere.
//
// Deliberately NOT a new kind of panel. It widens what a tab's SOURCE may be
// from a field to a path; the rows are still a list of one type rendered as a
// table, by the same code. One concept got slightly wider instead of a fourth
// rendering case — the renderer's rule count is the thing most at risk of
// becoming unholdable.
//
// Two segments only. Deeper is a graph browser, which is a different product,
// and the shallowness rule exists because a deep selection on a cyclic schema
// does not terminate.
const IncludeAnnotation = "include:"

// IncludePaths returns the dotted relationship paths a type declares.
func IncludePaths(typeDoc string) []string {
	v := annotationValue(typeDoc, IncludeAnnotation)
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		p := strings.TrimSpace(part)
		// A path with no separator names a field the type already holds, which
		// is a tab it already has. Silently ignored rather than duplicating it.
		if strings.Count(p, ".") == 1 {
			out = append(out, p)
		}
	}
	return out
}

// LabelAnnotation overrides the displayed name of a FIELD.
//
//	cni: String  # """label: CNI"""
//
// Labels are otherwise derived by title-casing the field name, which handles
// almost everything ("serviceTag" → "Service Tag") and cannot know an acronym:
// `cni` becomes "Cni" and `oobIP` becomes "Oob IP". The alternative was a
// hand-maintained list of exceptions in Go — the shape this whole layer exists
// to delete — so the exception lives with the field it describes, in the
// schema, like every other annotation.
//
// Only annotate what the rule gets WRONG. A label: repeating what title-casing
// already produces is a line that has to be kept in step for no gain.
const LabelAnnotation = "label:"

// LabelFor returns a field's declared label, or "".
func LabelFor(fieldDoc string) string {
	return annotationValue(fieldDoc, LabelAnnotation)
}

// DetailOnlyAnnotation keeps a field off TABLE columns while still rendering it
// on a detail page.
//
//	"""detailOnly"""
//	assetDataV2: String
//
// A data centre's assetDataV2 is ~600 characters of JSON; as a column it made
// the table unreadable and shoved every other column off the screen. On the
// detail page the document IS the content.
//
// Placement is stated, never inferred. This rule briefly lived in Go as
// "a jsonString field is never a column", which conflated WHAT a field holds
// with WHERE it may appear and left "actually, show it as a column"
// unexpressible. `jsonString` means only that the String holds JSON — it drives
// editor parsing and pretty-printing, and says nothing about placement.
const DetailOnlyAnnotation = "detailOnly"

// IsDetailOnly reports whether a field's docstring keeps it out of tables.
func IsDetailOnly(doc string) bool {
	return hasAnnotationLine(doc, DetailOnlyAnnotation)
}

// DetailOnlyFieldsFor returns a type's detail-only fields.
func DetailOnlyFieldsFor(info TypeInfo) []string {
	var out []string
	for _, f := range info.Fields {
		if IsDetailOnly(f.Doc) {
			out = append(out, f.Name)
		}
	}
	return out
}
