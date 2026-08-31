package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// EntityRef is what the graph currently knows about one orbId.
type EntityRef struct {
	OrbID     string
	Type      string
	Namespace string
}

// TypeSchema is the writable shape of one ConfigItem type, as the DEPLOYED
// schema declares it.
type TypeSchema struct {
	// Fields maps every settable field to whether it is an edge (a reference to
	// another ConfigItem) rather than a scalar.
	Fields map[string]FieldSchema
	// RequiredOnCreate are the fields a create must carry, excluding the ones
	// orbital stamps itself (namespace, orbId, version).
	RequiredOnCreate []string
}

// FieldSchema is one settable field.
type FieldSchema struct {
	// IsEdge is true for a reference to another ConfigItem. Edge values may
	// only carry an identity key — DGraph LINKS on an edge and silently
	// discards any other nested field, so a nested write looks like it applied
	// and did nothing.
	IsEdge bool
	// TypeName is the named GraphQL type (e.g. "String", "Int", "DataCenterRef").
	TypeName string
	// IsList is true for list-valued fields.
	IsList bool
}

// SchemaSource is what validation needs from the graph. An interface so
// validation is testable without DGraph, and so the source of truth can move
// (introspection today) without touching the rules.
type SchemaSource interface {
	// NamespaceExists reports whether a namespace holds anything. Used to refuse
	// a policy for a namespace that does not exist — one that governs nothing
	// while reporting itself enforced is the worst state a security control can
	// be in, and a single typo produces it.
	NamespaceExists(ctx context.Context, name string) (bool, error)
	// ResolveEntities returns what exists for the given orbIds. orbIds with no
	// entity are OMITTED from the result — absence is how a create is detected.
	ResolveEntities(ctx context.Context, orbIDs []string) (map[string]EntityRef, error)
	// TypeSchemas returns the writable shape of each named type. An unknown
	// type name is omitted.
	TypeSchemas(ctx context.Context, typeNames []string) (map[string]TypeSchema, error)
}

// DGraphSchemaSource answers both questions with GraphQL against DGraph.
//
// Deliberately introspection rather than parsing schema/schema.graphql: the
// file on disk is what this build would deploy, while introspection is what is
// actually deployed right now. A change request validated against the file
// could name a field the running graph does not have (or reject one it does) —
// after a restore from an older backup, or on a node running a prior schema
// version. It also keeps the "no runtime schema parser, no gqlparser
// dependency" position that internal/configitems takes.
type DGraphSchemaSource struct {
	url    string
	client *http.Client
}

func NewDGraphSchemaSource(dgraphURL string) *DGraphSchemaSource {
	return &DGraphSchemaSource{url: dgraphURL, client: http.DefaultClient}
}

func (s *DGraphSchemaSource) ResolveEntities(ctx context.Context, orbIDs []string) (map[string]EntityRef, error) {
	if len(orbIDs) == 0 {
		return map[string]EntityRef{}, nil
	}
	// One round-trip for the whole changeset. queryConfigItem works because
	// orbId is @id on the ConfigItem interface — globally unique across every
	// type, which is exactly what makes `type` inferable rather than required.
	const q = `query($ids:[String!]){ queryConfigItem(filter:{orbId:{in:$ids}}) { __typename orbId namespace } }`

	var out struct {
		QueryConfigItem []struct {
			Typename  string `json:"__typename"`
			OrbID     string `json:"orbId"`
			Namespace string `json:"namespace"`
		} `json:"queryConfigItem"`
	}
	if err := s.do(ctx, q, map[string]any{"ids": orbIDs}, &out); err != nil {
		return nil, fmt.Errorf("resolve entities: %w", err)
	}

	refs := make(map[string]EntityRef, len(out.QueryConfigItem))
	for _, r := range out.QueryConfigItem {
		refs[r.OrbID] = EntityRef{OrbID: r.OrbID, Type: r.Typename, Namespace: r.Namespace}
	}
	return refs, nil
}

func (s *DGraphSchemaSource) TypeSchemas(ctx context.Context, typeNames []string) (map[string]TypeSchema, error) {
	names := dedupeSorted(typeNames)
	if len(names) == 0 {
		return map[string]TypeSchema{}, nil
	}

	// <Type>Patch is the settable field set for an update; Add<Type>Input adds
	// the create-time requirements. Both in one aliased query.
	var b strings.Builder
	b.WriteString("{")
	for i, n := range names {
		fmt.Fprintf(&b, " p%d: __type(name:%q){ inputFields { name %s } }", i, n+"Patch", typeRefSelection)
		fmt.Fprintf(&b, " a%d: __type(name:%q){ inputFields { name %s } }", i, "Add"+n+"Input", typeRefSelection)
	}
	b.WriteString(" }")

	var raw map[string]*struct {
		InputFields []introspectedField `json:"inputFields"`
	}
	if err := s.do(ctx, b.String(), nil, &raw); err != nil {
		return nil, fmt.Errorf("introspect types: %w", err)
	}

	schemas := make(map[string]TypeSchema, len(names))
	for i, n := range names {
		patch := raw[fmt.Sprintf("p%d", i)]
		if patch == nil {
			continue // unknown type — the caller reports it
		}
		ts := TypeSchema{Fields: make(map[string]FieldSchema, len(patch.InputFields))}
		for _, f := range patch.InputFields {
			kind, name, isList := f.Type.unwrap()
			ts.Fields[f.Name] = FieldSchema{
				IsEdge:   kind == "INPUT_OBJECT",
				TypeName: name,
				IsList:   isList,
			}
		}
		if add := raw[fmt.Sprintf("a%d", i)]; add != nil {
			for _, f := range add.InputFields {
				if !f.Type.nonNull() || stampedFields[f.Name] {
					continue
				}
				ts.RequiredOnCreate = append(ts.RequiredOnCreate, f.Name)
			}
			sort.Strings(ts.RequiredOnCreate)
		}
		schemas[n] = ts
	}
	return schemas, nil
}

// typeRefSelection unwraps three levels, enough for `[Type!]!`.
const typeRefSelection = `type { kind name ofType { kind name ofType { kind name ofType { kind name } } } }`

type introspectedField struct {
	Name string      `json:"name"`
	Type introRefTyp `json:"type"`
}

type introRefTyp struct {
	Kind   string       `json:"kind"`
	Name   string       `json:"name"`
	OfType *introRefTyp `json:"ofType"`
}

// unwrap strips NON_NULL/LIST wrappers down to the named type.
func (t introRefTyp) unwrap() (kind, name string, isList bool) {
	cur := &t
	for cur != nil {
		if cur.Kind == "LIST" {
			isList = true
		}
		if cur.OfType == nil {
			return cur.Kind, cur.Name, isList
		}
		cur = cur.OfType
	}
	return "", "", isList
}

func (t introRefTyp) nonNull() bool { return t.Kind == "NON_NULL" }

func (s *DGraphSchemaSource) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("marshal query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post dgraph: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dgraph returned %d: %s", resp.StatusCode, truncate(string(respBytes), 300))
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("dgraph: %s", env.Errors[0].Message)
	}
	if len(env.Data) == 0 {
		return fmt.Errorf("dgraph returned no data")
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	return nil
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// NamespaceExists reports whether any ConfigItem carries this namespace.
//
// Deliberately "does anything live here", not "is there a Namespace node":
// a policy exists to gate writes to entities, so a namespace with no entities
// cannot be what the admin meant, whether or not a bare Namespace node happens
// to exist.
func (s *DGraphSchemaSource) NamespaceExists(ctx context.Context, name string) (bool, error) {
	const q = `query($ns:String!){ queryConfigItem(filter:{namespace:{eq:$ns}}, first:1) { orbId } }`
	var out struct {
		QueryConfigItem []struct {
			OrbID string `json:"orbId"`
		} `json:"queryConfigItem"`
	}
	if err := s.do(ctx, q, map[string]any{"ns": name}, &out); err != nil {
		return false, fmt.Errorf("check namespace %q: %w", name, err)
	}
	return len(out.QueryConfigItem) > 0, nil
}
