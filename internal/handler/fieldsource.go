package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/armada/orbital/internal/configitems"
)

// HandlerOption configures a page handler. Variadic and additive on purpose:
// the field source had to reach four constructors and 26 call sites, and a
// required parameter would have churned every test for a dependency most of
// them do not exercise.
type HandlerOption func(*handlerOptions)

type handlerOptions struct {
	fields configitems.FieldsFor
	meta   configitems.MetaFor
	state  *fieldResolverState
}

// WithFields supplies the editable field list. Production passes one shared
// resolver so the whole process holds a single cache; tests pass a fixture.
func WithFields(f configitems.FieldsFor) HandlerOption {
	return func(o *handlerOptions) { o.fields = f }
}

// WithMeta supplies per-type metadata. Tests that only need field lists can
// omit it; the fallback returns a zero TypeInfo, and orbIDSuffix then uses the
// lower-cased type name — which is the default for all but six types anyway.
func WithMeta(m configitems.MetaFor) HandlerOption {
	return func(o *handlerOptions) { o.meta = m }
}

// adminURLFor derives the /admin endpoint from a /graphql one. They are the
// same DGraph on the same host and orbital only ever configures the latter.
func adminURLFor(graphqlURL string) string {
	if strings.HasSuffix(graphqlURL, "/graphql") {
		return strings.TrimSuffix(graphqlURL, "/graphql") + "/admin"
	}
	return graphqlURL
}

// fieldsFrom returns the field source a handler should use.
//
// With no option it builds a resolver against the handler's own DGraph. That
// keeps every existing call site working, and means a handler is never
// constructed without SOME answer to "what is editable" — but note the answer
// then comes from a per-handler cache, so production must pass the shared one.
//
// On failure it returns nil for the type rather than an empty list. Empty reads
// as "this type has no editable fields", which is a different and misleading
// claim from "orbital cannot see the schema"; the caller checks Err to tell
// them apart.
func fieldsFrom(dgraphURL string, logger *slog.Logger, opts []HandlerOption) (configitems.FieldsFor, configitems.MetaFor, *fieldResolverState) {
	var o handlerOptions
	for _, opt := range opts {
		opt(&o)
	}
	meta := o.meta
	if meta == nil {
		meta = func(string) configitems.TypeInfo { return configitems.TypeInfo{} }
	}
	if o.fields != nil {
		if o.state != nil {
			return o.fields, meta, o.state
		}
		return o.fields, meta, &fieldResolverState{}
	}
	// No implicit resolver. Constructing one here would mean any handler built
	// without the option quietly starts introspecting DGraph — which surprised
	// four stub-based tests that count requests, and would have surprised
	// production the same way. The dependency is explicit: NewSharedFieldSource
	// in server wiring, a fixture in tests.
	//
	// The fallback is LOUD rather than silent: an empty field list reads as
	// "this type has no editable fields", which is a different and misleading
	// claim from "nobody told this handler where to look".
	var warned bool
	noFields := func(typeName string) []string {
		if !warned {
			warned = true
			if logger != nil {
				logger.Warn("page handler has no field source; the editor will offer no fields",
					"hint", "pass handler.WithFields(handler.NewSharedFieldSource(...)) when constructing it",
					"type", typeName)
			}
		}
		return nil
	}
	return noFields, meta, &fieldResolverState{}
}

// fieldResolverState carries the last resolution error so a page can say WHY
// the editor is unavailable instead of rendering an empty form.
type fieldResolverState struct {
	resolver *configitems.Resolver
	lastErr  error
}

// metaFor resolves per-type metadata, or a zero value when the schema is
// unreadable — callers then fall back to conventions rather than failing.
func (s *fieldResolverState) metaFor(typeName string) configitems.TypeInfo {
	if s.resolver == nil {
		return configitems.TypeInfo{}
	}
	info, err := s.resolver.TypeInfoFor(context.Background(), typeName)
	if err != nil {
		s.lastErr = err
		return configitems.TypeInfo{}
	}
	return info
}

func (s *fieldResolverState) fieldsFor(typeName string) []string {
	if s.resolver == nil {
		return nil
	}
	f, err := s.resolver.Fields(context.Background(), typeName)
	if err != nil {
		s.lastErr = err
		return nil
	}
	s.lastErr = nil
	return f
}

// Err reports the last field-resolution failure, for the page to surface.
func (s *fieldResolverState) Err() error { return s.lastErr }

// NewSharedFieldSource builds ONE field source for a whole process.
//
// Pass it to every page handler via WithFields. Without it each handler builds
// its own resolver and therefore its own cache, which multiplies introspection
// by the number of handlers and lets them briefly disagree about the schema
// after a change.
func NewSharedFieldSource(dgraphURL string, logger *slog.Logger) *SharedFields {
	r := configitems.NewResolver(
		configitems.NewDGraphSchemaClient(dgraphURL, adminURLFor(dgraphURL)),
		30*time.Second,
	).WithLogger(logger)
	sf := &SharedFields{st: &fieldResolverState{resolver: r}}
	// Children() and downwardEdges() are package functions with no resolver to
	// thread, so the interface lookup is wired once here. Set from the SHARED
	// source deliberately: a per-handler resolver would mean the last handler
	// constructed silently decided what every caller saw.
	configitems.SetImplementsLookup(func(typeName string) []string {
		return sf.Meta(typeName).Implements
	})
	return sf
}

// SharedFields is one process-wide field source together with its last
// resolution error.
//
// The error travels WITH the source deliberately: a handler that can render an
// empty editor needs to know the difference between "this type has no editable
// fields" and "orbital cannot see the schema". An earlier version returned the
// bare lookup function and dropped the error, which made that distinction
// unavailable at exactly the site that has to draw it.
type SharedFields struct{ st *fieldResolverState }

// Fields satisfies configitems.FieldsFor.
func (s *SharedFields) Fields(typeName string) []string { return s.st.fieldsFor(typeName) }

// State exposes the resolver's last error so a page can say WHY the editor is
// unavailable rather than rendering an empty form.
func (s *SharedFields) State() *fieldResolverState { return s.st }

// Meta satisfies configitems.MetaFor: the schema-derived per-type metadata
// (JSON-in-a-String fields, orbId suffix) that used to be declared by hand.
func (s *SharedFields) Meta(typeName string) configitems.TypeInfo { return s.st.metaFor(typeName) }

// Views returns the resolved view list, from the same cache as the field sets.
func (s *SharedFields) Views(ctx context.Context) ([]configitems.View, error) {
	if s.st.resolver == nil {
		return nil, errNoFieldSource
	}
	return s.st.resolver.Views(ctx)
}

var errNoFieldSource = errors.New("no schema source configured")

// WithFieldSource supplies a shared source and keeps its error reachable.
func WithFieldSource(sf *SharedFields) HandlerOption {
	return func(o *handlerOptions) {
		o.fields = sf.Fields
		o.meta = sf.Meta
		o.state = sf.st
	}
}

// editorUnavailableReason renders a user-facing reason, or "" when the field
// list resolved. Call it AFTER building edit targets: the state records the last
// lookup, and the lookups happen while the targets are built.
func editorUnavailableReason(st *fieldResolverState) string {
	if st == nil || st.Err() == nil {
		return ""
	}
	return "Orbital cannot read the schema from DGraph, so it does not know which fields are editable. " +
		"Editing will work again as soon as DGraph is reachable — no restart needed. (" + st.Err().Error() + ")"
}
