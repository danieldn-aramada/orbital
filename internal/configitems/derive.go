package configitems

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
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

	// OrbIDPattern is the type's declared orbId shape, parsed. Several entries
	// are ALTERNATIVES — see OrbIDPatternAnnotation in orbid.go.
	OrbIDPattern []OrbIDPattern

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

	// NonNull is the OUTERMOST wrapper — `Server!`, not `[Server!]`.
	//
	// It is where CONTAINMENT comes from. `Child.parent: Parent!` is the schema
	// saying the child has no existence without that parent, so it dies with it
	// and its audit rolls up onto it. 13 of orbital's 14 ownership relations
	// derive from this; the one that cannot is NetworkInterface, whose owner is
	// an XOR across three nullable edges.
	NonNull bool
}

// BeforeSelection is the audit before-fetch for one type: its own derived
// fields, FLAT.
//
// Flat because the mutation decides the diff, not this selection: `changes` is
// keys(before) ∩ keys(set) (computeChanges), so a before field the mutation
// never set is dropped, and one mutation only ever reaches one type's scalars —
// a nested object in `set` links by @id and its values are discarded
// (DGRAPH.md § "A nested child update in a `set` is SILENTLY DISCARDED").
// Selecting owned children here widened the stored `before` snapshot with
// fields no diff could reach.
//
// A parent's Audit Log tab still carries its children's events. That roll-up is
// read-side and by orbId — see ownedSubtreeOrbIDs.
func BeforeSelection(typeName string, fields FieldsFor) string {
	sel := "id orbId name version"
	for _, f := range fields(typeName) {
		if f == "name" {
			continue // already in the head
		}
		sel += " " + f
	}
	return sel
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

	// The views document's two file paths and the schema version to compare it
	// against. Watched on the SAME rate-limited loop as the deployed SDL, which
	// is what lets a ConfigMap edit reach the pages without a rollout restart:
	// the kubelet updates a volume-mounted ConfigMap in place, and the next
	// check sees a different hash.
	viewsPath     string
	overlayPath   string
	schemaVersion string

	mu          sync.RWMutex
	derived     map[string][]string
	typeNames   []string
	mutationRe  *regexp.Regexp
	inverse     map[string]string
	snapshot    map[string]TypeInfo
	ifaceFields []string
	viewConfig  ViewConfig
	views       []View
	viewsErr    error
	sdlHash     string
	viewsHash   string
	lastCheck   time.Time
	resolved    bool
}

func NewResolver(client SchemaClient, checkEvery time.Duration) *Resolver {
	return &Resolver{client: client, checkEvery: checkEvery, now: time.Now}
}

// WithViews points the resolver at the shipped views document and its optional
// per-deployment overlay.
//
// schemaVersion is the label the config is checked against — the COARSE signal
// of §5. It catches the case it was built for, an overlay authored against an
// earlier release, and it cannot catch two schemas sharing a version label,
// because schema/VERSION is bumped by hand and deliberately not for every
// change. Per-member validation against live introspection is what actually
// catches a stale declaration; this only says "look".
func (r *Resolver) WithViews(viewsPath, overlayPath, schemaVersion string) *Resolver {
	r.viewsPath, r.overlayPath, r.schemaVersion = viewsPath, overlayPath, schemaVersion
	return r
}

// ViewsHash identifies the views document the current view list was built from.
//
// The editor stamps it into its payload and sends it back on save, so an edit
// opened under one view and saved under another is refused rather than silently
// emitting a `remove` for an entity the view no longer carries. Empty until the
// first successful resolve.
func (r *Resolver) ViewsHash() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.viewsHash
}

// InverseOf returns the field on the FAR type that points back along an edge —
// the `@hasInverse` partner of `typeName.field`, or "" when the edge declares
// none.
//
// Read from the SDL, because introspection cannot see it: `__Field` exposes
// name/description/args/type/isDeprecated and no applied directives at all. The
// SDL is already fetched for the cache-invalidation hash, so this costs nothing
// extra.
//
// It is what lets a delete clear the edges held by SURVIVORS without a
// hand-maintained list. A DQL delete does NOT maintain `@hasInverse` — orbital's
// cascade is a DQL upsert, deliberately, because that is the only way to get a
// version-guarded CAS — so an `S * *` delete clears the child and leaves the
// parent pointing at an empty uid. Any later query selecting a non-nullable
// field through that edge then fails ENTIRELY, which is how one cluster delete
// permanently broke export for its whole data centre.
func (r *Resolver) InverseOf(ctx context.Context, typeName, field string) string {
	if err := r.ensure(ctx); err != nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.inverse[typeName+"."+field]
}

// MutationRegex matches every add/update/delete mutation against a type the
// DEPLOYED schema declares as a ConfigItem.
//
// It gates three things, and the asymmetry between its two failure modes is what
// decides the design:
//
//   - MISSING a type is catastrophic and silent: the approval gate does not run
//     (an ungated write) and no audit event is recorded (a hole nobody can see).
//   - MATCHING too much is harmless: the audit path runs only on mutations DGraph
//     ACCEPTED, so the type exists; the approval gate only ever refuses or passes.
//
// So it must never under-match. When the schema cannot be read it returns the
// BROAD pattern rather than an empty or stale one — over-auditing beats a silent
// gap, every time.
//
// ⚠️ This replaced a hand-maintained Go list, and not only for tidiness. That
// list was keyed to the SHIPPED schema while the gate operates on the DEPLOYED
// one. A deployment whose DGraph carried a ConfigItem type the shipped file did
// not would get ungated, unaudited writes on it — and no build-time test can see
// that, because the drift is between an environment and a binary, not between
// two files in one repo.
func (r *Resolver) MutationRegex(ctx context.Context) *regexp.Regexp {
	if err := r.ensure(ctx); err != nil {
		return BroadMutationRegex
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mutationRe == nil {
		return BroadMutationRegex
	}
	return r.mutationRe
}

// TypeNames returns the ConfigItem type names the deployed schema declares,
// sorted. Used where a caller offers or validates the set of types.
func (r *Resolver) TypeNames(ctx context.Context) ([]string, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.typeNames...), nil
}

// CurrentViewsHash is ViewsHash after a rate-limited re-check.
//
// The difference matters on the WRITE path. ViewsHash reads the cached value, so
// it answers with whatever the last resolve saw — fine for stamping a page that
// just rendered, and useless for deciding whether a submitted page is stale,
// which is the one question that needs the current answer. Both exist because
// both are correct for their caller.
//
// A failure returns "" rather than an error: the comparison is a guard, and a
// guard that cannot read the configuration must not refuse every write.
func (r *Resolver) CurrentViewsHash(ctx context.Context) string {
	if err := r.ensure(ctx); err != nil {
		return ""
	}
	return r.ViewsHash()
}

// ViewConfig returns the merged, validated views document.
func (r *Resolver) ViewConfig(ctx context.Context) (ViewConfig, error) {
	if err := r.ensure(ctx); err != nil {
		return ViewConfig{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.viewConfig, nil
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

	// The views document is read on the same loop. Two small file reads at most
	// once per checkEvery, which is what makes "edit the ConfigMap, see the
	// change" true without a watch, an inotify loop, or a restart.
	cfg, cfgHash, cfgWarn, cfgErr := LoadViewConfig(r.viewsPath, r.overlayPath)
	if cfgErr != nil {
		if resolved {
			// Keep serving the last good document. A views file that briefly
			// cannot be read must not take every page down with it.
			return nil
		}
		return fmt.Errorf("cannot read the views configuration, so no page knows what to render: %w", cfgErr)
	}

	r.mu.RLock()
	unchanged := resolved && hash == r.sdlHash && cfgHash == r.viewsHash
	cachedTypes, cachedIface := r.snapshot, r.ifaceFields
	schemaMoved := !resolved || hash != r.sdlHash
	r.mu.RUnlock()
	if unchanged {
		r.mu.Lock()
		r.lastCheck = r.now()
		r.mu.Unlock()
		return nil
	}

	types, iface := cachedTypes, cachedIface
	if schemaMoved {
		types, iface, err = r.client.Introspect(ctx)
		if err != nil {
			if resolved {
				return nil
			}
			return fmt.Errorf("cannot introspect the schema, so the editable fields are unknown: %w", err)
		}
	}

	// Validated against LIVE introspection, never against the schema file: the
	// two can be different versions, which is the whole exposure a separate
	// views document creates. Unsupportable declarations are dropped here, and
	// reported below — never fatal, never silent.
	validated, viewWarn := cfg.Validate(types, r.schemaVersion)
	views, viewsErr := ResolveViewsFromConfig(types, iface, validated)

	// ONE editable-field set, built from the views. The editor's field list and
	// the audit before-fetch both read it, and a second derivation beside the
	// View's own `Fields` would be two answers to "what may a human write here"
	// — which is exactly the drift that made the audit before-fetch a
	// Hi-severity debt row when it was hand-maintained.
	derived := make(map[string][]string, len(views))
	for _, v := range views {
		derived[v.Type] = v.Fields
	}

	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)

	r.mu.Lock()
	r.derived = derived
	r.inverse = InverseEdges(sdl)
	r.typeNames = names
	r.mutationRe = MutationRegexFor(names)
	r.snapshot = types
	r.ifaceFields = iface
	r.viewConfig = validated
	r.views, r.viewsErr = views, viewsErr
	r.sdlHash = hash
	r.viewsHash = cfgHash
	r.lastCheck = r.now()
	r.resolved = true
	r.mu.Unlock()

	// Logged on EVERY resolve, not only the first. This covers boot and every
	// later change to either artifact, and a views document whose declarations
	// went missing after a redeploy is exactly as invisible as one that never
	// had them — the count is what makes zero-when-expecting-N obvious.
	if r.logger != nil {
		members := 0
		var pages []string
		for _, v := range views {
			members += len(v.Tabs)
			if v.Slug != "" {
				pages = append(pages, v.Slug)
			}
		}
		sort.Strings(pages)
		// The SLUGS, not just a count. This log line is the only place the
		// RESOLVED set is legible: the shipped config is baked into the image,
		// a deployment's overlay is a separate ConfigMap, and the derived half
		// comes from DGraph — so the merged result exists nowhere but in this
		// process. A /views page and a GET /api/v1/views both used to answer
		// this and were deleted 2026-10-05: the question is one a developer
		// asks while debugging, and this is where they already are.
		r.logger.Info("resolved views against the deployed schema",
			"views", len(views), "members", members, "types", len(types),
			"pages", strings.Join(pages, ","),
			"schema_version", r.schemaVersion, "views_hash", shortHash(cfgHash))
		for _, w := range cfgWarn {
			r.logger.Warn("views configuration", "detail", w)
		}
		// A declaration the deployed schema cannot support is DROPPED, so the
		// tab, column or filter simply is not there — indistinguishable from the
		// outside from never having declared it. That is why it is said out loud.
		for _, w := range viewWarn {
			r.logger.Warn("views declaration dropped; the deployed schema cannot support it", "where", w)
		}
		for _, w := range FilterByWarningsFromConfig(views, validated) {
			r.logger.Warn("filterBy dropped; the list page has no filter dropdown for it", "where", w)
		}
		for _, w := range ColumnWarningsFromConfig(types, iface, validated) {
			r.logger.Warn("column dropped; it will not render", "where", w)
		}
		// Annotation typos stay worth reporting: the three that remain in the
		// schema are identity and data facts, and a misspelled one fails as
		// silently as any other docstring — more so now that there are only
		// three, with no near-neighbours to make a misspelling look odd.
		for _, u := range UnknownAnnotationsIn(types) {
			r.logger.Warn("unrecognised orbital annotation in the deployed schema; it does nothing",
				"where", u)
		}
		// Reported apart from a generic annotation typo because the consequence
		// differs: a misspelled annotation does nothing, while a broken
		// orbIdPattern is an identity orbital can neither construct nor verify.
		for _, w := range OrbIDPatternWarnings(types) {
			r.logger.Warn("orbIdPattern unusable; orbital cannot construct or verify this type's orbId",
				"where", w)
		}
	}
	return nil
}

// shortHash abbreviates a content hash for a log line, git-style. The whole
// digest in every resolve line is noise; eight characters is enough to see that
// it moved, which is the only question the line answers.
func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// WithLogger attaches a logger so each re-derive reports what it resolved.
func (r *Resolver) WithLogger(l *slog.Logger) *Resolver {
	r.logger = l
	return r
}

var knownAnnotations = []string{
	JSONStringAnnotation,   // bare word         — what this String CONTAINS
	OrbIDSuffixAnnotation,  // "orbIdSuffix:"    — identity
	OrbIDPatternAnnotation, // "orbIdPattern:"   — identity
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

// UnknownAnnotationsIn reports every docstring line in the deployed schema that
// LOOKS like an orbital annotation and matches none, as "Type.field: text".
//
// The vocabulary is three words now, which makes a typo MORE likely to pass
// unnoticed rather than less: there is no longer a crowd of near-neighbours to
// make a misspelling look odd. `"""orbIdSufix: idrac"""` is a perfectly valid
// docstring that silently derives the wrong orbId for every child of that type.
func UnknownAnnotationsIn(types map[string]TypeInfo) []string {
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, typeName := range names {
		info := types[typeName]
		if info.IsInterface {
			// A sub-interface's annotations are INHERITED by each implementing
			// type and already reported there; counting the interface too
			// reports every one of them twice.
			continue
		}
		for _, u := range UnknownAnnotations(info.Doc) {
			out = append(out, typeName+": "+u)
		}
		for _, f := range info.Fields {
			for _, u := range UnknownAnnotations(f.Doc) {
				out = append(out, typeName+"."+f.Name+": "+u)
			}
		}
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

const MenuWeightUnpinned = 1 << 20

// MutationRegexFor builds the add/update/delete matcher for a set of type names.
//
// Case-insensitive because DGraph's generated mutation is `addServer` while a
// query may spell the operation `AddServer`; `\b` anchors so `addServerThing`
// does not match `addServer`.
func MutationRegexFor(names []string) *regexp.Regexp {
	if len(names) == 0 {
		return BroadMutationRegex
	}
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, regexp.QuoteMeta(n))
	}
	return regexp.MustCompile(`(?i)\b(add|update|delete)(` + strings.Join(quoted, "|") + `)\b`)
}

// BroadMutationRegex matches an add/update/delete against ANY capitalised name.
//
// The degraded answer, used when the deployed schema cannot be read. It
// over-matches deliberately: the cost is an audit row for a type orbital does not
// know about, and the alternative is an ungated, unaudited write — which is the
// one outcome this whole path exists to prevent.
//
// ⚠️ The TYPE half is case-SENSITIVE while the verb half is not, and that is
// load-bearing. Written `(?i)...([A-Z]\w*)`, the `(?i)` makes `[A-Z]` match a
// lowercase letter too — so `updatedAt` parses as `update` + `dAt` and matches.
// `updatedAt` is in the selection set of essentially every query, so a plain READ
// would have been treated as a mutation: the approval gate would run on it and
// the audit pipeline would try to record it. The named-type regex does not have
// this problem, because a type name anchors what follows the verb.
var BroadMutationRegex = regexp.MustCompile(`\b([aA]dd|[uU]pdate|[dD]elete)([A-Z]\w*)\b`)

// InverseEdges maps "Type.field" to the field on the far type that points back,
// parsed from `@hasInverse` in the SDL.
//
// A deliberately small line scanner rather than a GraphQL parser: the directive
// only ever appears on one line with its field, and taking a parser dependency
// to read one annotation is the trade this codebase has refused elsewhere.
//
// Both directions are recorded. The SDL declares the directive on one end only,
// but a caller asking "what points back at me" has no way to know which end that
// was.
func InverseEdges(sdl string) map[string]string {
	out := map[string]string{}
	cur := ""
	for _, ln := range strings.Split(sdl, "\n") {
		if m := declRe.FindStringSubmatch(ln); m != nil {
			cur = m[1]
			continue
		}
		if cur == "" {
			continue
		}
		m := hasInverseRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		field, target, far := m[1], m[2], m[3]
		out[cur+"."+field] = far
		out[target+"."+far] = field
	}
	return out
}

var hasInverseRe = regexp.MustCompile(`^\s+(\w+):\s*\[?(\w+)\]?!?\s+@hasInverse\(field:\s*(\w+)\)`)
