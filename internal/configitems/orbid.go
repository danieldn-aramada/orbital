package configitems

import (
	"fmt"
	"sort"
	"strings"
)

// OrbIDPatternAnnotation declares the shape of a type's orbId.
//
//	"""orbIdPattern: {kind}-{serviceTag}"""
//	type Server implements ConfigItem {
//
// The value is everything AFTER the `<namespace>:` prefix. `{...}` interpolates;
// literal text is copied through. Three placeholder forms:
//
//	{kind}              the type's kind token — `orbIdSuffix:` if annotated,
//	                    otherwise the lower-cased type name
//	{field}             a scalar on this type
//	{edge.field}        a scalar on the entity this type points at
//
// REPEAT the line to declare alternatives, for a type whose identity hangs off
// whichever of several owners it actually has:
//
//	"""
//	orbIdPattern: {kind}-{server.serviceTag}-{fqdd}
//	orbIdPattern: {kind}-{networkDevice.serial}-{port}
//	"""
//
// This is what `derivesIdFrom:` could not express — it was single-valued, which
// is why NetworkInterface never carried one and why the field it populated was
// deleted without ever having been read.
//
// `{kind}` is a PLACEHOLDER rather than an automatic prefix because the legacy
// shapes do not all put it in front: IdracSettings is `<ns>:<serviceTag>-idrac`
// and Rack is `<ns>:<rackName>` with no kind token at all. A pattern that
// positions it explicitly can describe what is actually stored today, so the
// annotation doubles as the migration tracker — change the pattern, fix the
// data, and the audit tells you when the two agree.
const OrbIDPatternAnnotation = "orbIdPattern:"

// OrbIDPatternExternal opts a type out: its orbId is assigned elsewhere and
// orbital neither constructs nor checks it.
//
// Explicit, greppable, and the only accepted way to have no pattern — the
// build-time guard in schema_consistency_test.go fails on a ConfigItem type
// that declares neither. Modelled on Kubernetes `metadata.name` vs
// `generateName`: defaulting exists, but you have to ask for it.
const OrbIDPatternExternal = "external"

// OrbIDKindFor returns the token `{kind}` resolves to: the `orbIdSuffix:`
// annotation when the type carries one, otherwise the KEBAB type name.
//
// Kebab, deliberately — `network-device`, not the `networkdevice` that
// OrbIDSuffixFor falls back to. The two defaults differ because they answer
// different questions: OrbIDSuffixFor names a token the EDITOR appends to a
// parent's id, while this reproduces the documented
// `<namespace>:<kind>-<natural-key>` convention, which has always been kebab
// (`network-adapter-`, `server-maintenance-`). Reusing the lower-cased default
// here would have made every multi-word type's pattern wrong against the graph.
func OrbIDKindFor(typeName, typeDoc string) string {
	if v := annotationValue(typeDoc, OrbIDSuffixAnnotation); v != "" {
		return v
	}
	return KebabTypeName(typeName)
}

// orbIDSegment is one piece of a parsed pattern: literal text, or a placeholder
// path to resolve. Exactly one of the two is set.
type orbIDSegment struct {
	literal string
	path    string
}

// OrbIDPattern is a parsed `orbIdPattern:` declaration.
type OrbIDPattern struct {
	// Raw is the annotation value as written, for error messages.
	Raw string
	// Segments are the pattern in order.
	Segments []orbIDSegment
	// External marks `orbIdPattern: external`.
	External bool
}

// Paths returns every placeholder path the pattern references, in order.
func (p OrbIDPattern) Paths() []string {
	var out []string
	for _, s := range p.Segments {
		if s.path != "" {
			out = append(out, s.path)
		}
	}
	return out
}

// ParseOrbIDPattern parses one annotation value.
//
// Refuses an unbalanced or empty placeholder rather than treating it as
// literal text. A pattern is a statement about identity, and a malformed one
// that silently became a literal `{serviceTag}` in every orbId would be the
// exact failure this annotation exists to prevent.
func ParseOrbIDPattern(raw string) (OrbIDPattern, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return OrbIDPattern{}, fmt.Errorf("empty pattern")
	}
	if v == OrbIDPatternExternal {
		return OrbIDPattern{Raw: v, External: true}, nil
	}
	p := OrbIDPattern{Raw: v}
	for i := 0; i < len(v); {
		switch v[i] {
		case '{':
			end := strings.IndexByte(v[i:], '}')
			if end < 0 {
				return OrbIDPattern{}, fmt.Errorf("unclosed %q in %q", "{", v)
			}
			path := strings.TrimSpace(v[i+1 : i+end])
			if path == "" {
				return OrbIDPattern{}, fmt.Errorf("empty placeholder in %q", v)
			}
			// Depth is NOT capped. A one-hop cap looked tidy and is wrong on the
			// real graph: StorageVolume's orbId carries its controller's
			// SERVER's serviceTag, and EtcdBackup's carries its ClusterBackup's
			// CLUSTER's name. Both are two hops, and both are what is stored.
			p.Segments = append(p.Segments, orbIDSegment{path: path})
			i += end + 1
		case '}':
			return OrbIDPattern{}, fmt.Errorf("unmatched %q in %q", "}", v)
		default:
			// A literal run must contain no `}`. Without this check a stray
			// close brace is swallowed as literal text — `kind}-{serviceTag}`
			// parses happily and builds an orbId with a `}` in it.
			next := strings.IndexByte(v[i:], '{')
			lit := v[i:]
			if next >= 0 {
				lit = v[i : i+next]
			}
			if strings.ContainsRune(lit, '}') {
				return OrbIDPattern{}, fmt.Errorf("unmatched %q in %q", "}", v)
			}
			p.Segments = append(p.Segments, orbIDSegment{literal: lit})
			if next < 0 {
				i = len(v)
				continue
			}
			i += next
		}
	}
	return p, nil
}

// OrbIDPatternsFor returns a type's parsed patterns, in declaration order.
//
// Several `orbIdPattern:` lines in one docstring are ALTERNATIVES, so this
// returns a slice where the other annotation readers return a single value.
func OrbIDPatternsFor(typeDoc string) ([]OrbIDPattern, error) {
	var out []OrbIDPattern
	for _, raw := range annotationValues(typeDoc, OrbIDPatternAnnotation) {
		p, err := ParseOrbIDPattern(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// annotationValues returns EVERY `prefix: value` line, where annotationValue
// returns only the first. Repetition is meaningful for `orbIdPattern:` and for
// nothing else, which is why this is here rather than next to its single-valued
// sibling in derive.go.
func annotationValues(doc, prefix string) []string {
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, prefix) {
			continue
		}
		if v := strings.TrimSpace(strings.TrimPrefix(t, prefix)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// OrbIDPatternWarnings reports every type whose `orbIdPattern:` is malformed or
// names a field the deployed schema does not have.
//
// Reported, not fatal. The schema is applied to DGraph independently of the
// binary, so a strict parse here would let a bad schema someone else applied
// crashloop orbital. The build-time guard in schema_consistency_test.go is the
// layer that refuses — it reads the file before it ever ships, which is where
// a malformed pattern is cheap to fix. Mirrors UnknownAnnotationsIn.
func OrbIDPatternWarnings(types map[string]TypeInfo) []string {
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []string
	for _, typeName := range names {
		info := types[typeName]
		if info.IsInterface {
			// Inherited by each implementor and already reported there.
			continue
		}
		own := make(map[string]DerivedField, len(info.Fields))
		for _, f := range info.Fields {
			own[f.Name] = f
		}
		for _, raw := range annotationValues(info.Doc, OrbIDPatternAnnotation) {
			p, err := ParseOrbIDPattern(raw)
			if err != nil {
				out = append(out, typeName+": "+err.Error())
				continue
			}
			if p.External {
				continue
			}
			for _, path := range p.Paths() {
				if path == "kind" {
					continue
				}
				// Only the NEAR end is checkable here: the far end of
				// `{edge.field}` lives on another type, and resolving it needs
				// the edge's target, which this map does not carry per field.
				// The build-time guard in schema_consistency_test.go walks the
				// whole path, because it has the file.
				head, _, crosses := strings.Cut(path, ".")
				fd, found := own[head]
				switch {
				case !found:
					out = append(out, fmt.Sprintf("%s: orbIdPattern %q references %q, which is not a field on %s",
						typeName, p.Raw, head, typeName))
				case fd.IsList:
					// A list edge cannot say WHICH row, and a list scalar cannot
					// be part of one id.
					out = append(out, fmt.Sprintf("%s: orbIdPattern %q uses %q, which is a LIST and cannot be part of an id",
						typeName, p.Raw, head))
				case !crosses && fd.Kind != "SCALAR" && fd.Kind != "ENUM":
					// A bare relationship interpolates a node, not a value.
					out = append(out, fmt.Sprintf("%s: orbIdPattern %q interpolates %q, which is a relationship — "+
						"name a field on it (%s.<field>)", typeName, p.Raw, head, head))
				}
			}
		}
	}
	return out
}

// OrbIDResolver supplies the values a pattern interpolates.
//
// `kind` is handed over rather than derived here so construction stays a pure
// function of what it is given — the caller already knows the type, and a
// resolver that had to look one up could disagree with the view that named it.
type OrbIDResolver func(path string) (string, bool)

// ConstructOrbID builds the full `<namespace>:<pattern>` orbId.
//
// With several patterns the first whose placeholders ALL resolve wins; that is
// how an XOR-parented type picks the owner it actually has. When none resolves
// it returns an error naming every candidate, because a half-interpolated id is
// worse than no id: it would be accepted by DGraph, look approximately right,
// and collide or orphan later.
//
// Validation is this function plus a string compare, never a reverse parse. A
// stored orbId cannot be split back into its parts — `network-adapter-` is
// followed by a serviceTag and an FQDD, and FQDDs contain hyphens
// (`NIC.Integrated.1-1-1`), so the boundary is not recoverable.
func ConstructOrbID(namespace, kind string, patterns []OrbIDPattern, resolve OrbIDResolver) (string, error) {
	if namespace == "" {
		return "", fmt.Errorf("namespace is required")
	}
	if len(patterns) == 0 {
		return "", fmt.Errorf("no orbIdPattern declared")
	}
	var tried []string
	for _, p := range patterns {
		if p.External {
			return "", fmt.Errorf("orbIdPattern is %q: this type's orbId is assigned elsewhere", OrbIDPatternExternal)
		}
		s, ok := buildOne(kind, p, resolve)
		if ok {
			return namespace + ":" + s, nil
		}
		tried = append(tried, p.Raw)
	}
	return "", fmt.Errorf("no orbIdPattern resolved; tried %s", strings.Join(tried, " | "))
}

// buildOne interpolates one pattern, reporting false if any placeholder is
// unresolvable or resolves empty. An empty value is a failure, not a blank
// segment — `server-` with nothing after it is not an identity.
func buildOne(kind string, p OrbIDPattern, resolve OrbIDResolver) (string, bool) {
	var b strings.Builder
	for _, s := range p.Segments {
		if s.path == "" {
			b.WriteString(s.literal)
			continue
		}
		if s.path == "kind" {
			if kind == "" {
				return "", false
			}
			b.WriteString(kind)
			continue
		}
		v, ok := resolve(s.path)
		if !ok || v == "" {
			return "", false
		}
		b.WriteString(v)
	}
	return b.String(), true
}
