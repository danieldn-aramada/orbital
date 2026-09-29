package configitems

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// DGraphSchemaClient reads the schema from a running DGraph over its two
// channels. Both are needed and neither substitutes for the other:
//
//   - /admin  getGQLSchema -> the SDL verbatim, comments and directives intact.
//     The ONLY place directive applications (@hasInverse) are visible.
//   - /graphql introspection -> types, field kinds, and docstrings as
//     `description`. Verified 2026-09-24: __Field exposes exactly
//     name/description/args/type/isDeprecated/deprecationReason — there is NO
//     appliedDirectives, so directives cannot be read here.
type DGraphSchemaClient struct {
	graphqlURL string
	adminURL   string
	http       *http.Client
}

func NewDGraphSchemaClient(graphqlURL, adminURL string) *DGraphSchemaClient {
	return &DGraphSchemaClient{
		graphqlURL: graphqlURL,
		adminURL:   adminURL,
		http:       &http.Client{Timeout: 10 * time.Second},
	}
}

type gqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *DGraphSchemaClient) post(ctx context.Context, url, query string, out any) error {
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return fmt.Errorf("marshal query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", url, resp.StatusCode)
	}

	var env gqlEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("decode response from %s: %w", url, err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("%s: %s", url, env.Errors[0].Message)
	}
	return json.Unmarshal(env.Data, out)
}

// DeployedSDL returns the SDL as DGraph currently holds it.
func (c *DGraphSchemaClient) DeployedSDL(ctx context.Context) (string, error) {
	var out struct {
		GetGQLSchema *struct {
			Schema string `json:"schema"`
		} `json:"getGQLSchema"`
	}
	if err := c.post(ctx, c.adminURL, `{ getGQLSchema { schema } }`, &out); err != nil {
		return "", err
	}
	if out.GetGQLSchema == nil {
		// A DGraph that is up but carries no GraphQL schema. Distinct from
		// unreachable, and the caller must not treat it as "no fields".
		return "", fmt.Errorf("DGraph has no GraphQL schema applied")
	}
	return out.GetGQLSchema.Schema, nil
}

// TypeRef is a GraphQL type reference, which nests through NON_NULL and LIST
// wrappers before reaching the named type.
type TypeRef struct {
	Kind   string   `json:"kind"`
	Name   *string  `json:"name"`
	OfType *TypeRef `json:"ofType"`
}

// Unwrap walks the wrappers to the named type, reporting whether a LIST was
// crossed. A list of scalars is not the same editing affordance as a scalar.
func (t *TypeRef) Unwrap() (kind, name string, isList bool) {
	for cur := t; cur != nil; cur = cur.OfType {
		if cur.Kind == "LIST" {
			isList = true
		}
		if cur.Name != nil && *cur.Name != "" {
			kind, name = cur.Kind, *cur.Name
		}
	}
	return kind, name, isList
}

type introspectedField struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Type        TypeRef `json:"type"`
}

// fieldSelection asks for enough ofType depth to unwrap [X!]! and friends.
const fieldSelection = `fields { name description type { kind name ofType { kind name ofType { kind name ofType { kind name } } } } }`

// Introspect returns every ConfigItem implementing type's fields, and the
// interface's own field names.
func (c *DGraphSchemaClient) Introspect(ctx context.Context) (map[string]TypeInfo, []string, error) {
	var iface struct {
		Type *struct {
			Fields        []introspectedField `json:"fields"`
			PossibleTypes []struct {
				Name string `json:"name"`
			} `json:"possibleTypes"`
		} `json:"__type"`
	}
	if err := c.post(ctx, c.graphqlURL, `{ __type(name: "ConfigItem") { `+fieldSelection+` possibleTypes { name } } }`, &iface); err != nil {
		return nil, nil, err
	}
	if iface.Type == nil {
		return nil, nil, fmt.Errorf("the deployed schema has no ConfigItem interface")
	}

	ifaceFields := make([]string, 0, len(iface.Type.Fields))
	for _, f := range iface.Type.Fields {
		ifaceFields = append(ifaceFields, f.Name)
	}

	// Annotations declared on an INTERFACE do not reach the implementing type
	// through introspection — verified 2026-09-25: `"""editorIgnored"""` on
	// KubernetesCluster.description reads back as "editorIgnored" on the
	// interface and as "" on EksaKubernetesCluster. DGraph also forbids
	// redeclaring an interface field on the implementor, so the annotation
	// cannot simply be moved down. The reader therefore inherits it.
	ifaceDocs := map[string]map[string]string{} // interface -> field -> doc

	types := make(map[string]TypeInfo, len(iface.Type.PossibleTypes))
	for _, pt := range iface.Type.PossibleTypes {
		var obj struct {
			Type *struct {
				Description *string             `json:"description"`
				Fields      []introspectedField `json:"fields"`
				Interfaces  []struct {
					Name string `json:"name"`
				} `json:"interfaces"`
			} `json:"__type"`
		}
		if err := c.post(ctx, c.graphqlURL, `{ __type(name: "`+pt.Name+`") { description `+fieldSelection+` interfaces { name } } }`, &obj); err != nil {
			return nil, nil, fmt.Errorf("introspect %s: %w", pt.Name, err)
		}
		if obj.Type == nil {
			return nil, nil, fmt.Errorf("type %s is in ConfigItem.possibleTypes but cannot be introspected", pt.Name)
		}

		inherited := map[string]string{}
		for _, in := range obj.Type.Interfaces {
			docs, ok := ifaceDocs[in.Name]
			if !ok {
				var ifc struct {
					Type *struct {
						Fields []introspectedField `json:"fields"`
					} `json:"__type"`
				}
				if err := c.post(ctx, c.graphqlURL, `{ __type(name: "`+in.Name+`") { `+fieldSelection+` } }`, &ifc); err != nil {
					return nil, nil, fmt.Errorf("introspect interface %s: %w", in.Name, err)
				}
				docs = map[string]string{}
				if ifc.Type != nil {
					for _, f := range ifc.Type.Fields {
						if f.Description != nil && *f.Description != "" {
							docs[f.Name] = *f.Description
						}
					}
				}
				ifaceDocs[in.Name] = docs
			}
			for name, doc := range docs {
				if _, seen := inherited[name]; !seen {
					inherited[name] = doc
				}
			}
		}

		typeDoc := ""
		if obj.Type.Description != nil {
			typeDoc = *obj.Type.Description
		}
		implements := make([]string, 0, len(obj.Type.Interfaces))
		for _, in := range obj.Type.Interfaces {
			implements = append(implements, in.Name)
		}
		payloadField, err := c.payloadFieldFor(ctx, pt.Name)
		if err != nil {
			return nil, nil, err
		}
		types[pt.Name] = TypeInfo{
			Doc:          typeDoc,
			OrbIDSuffix:  OrbIDSuffixFor(pt.Name, typeDoc),
			Fields:       toDerivedFieldsWithInherited(obj.Type.Fields, inherited),
			Implements:   implements,
			PayloadField: payloadField,
		}
	}

	// Sub-interfaces become views of their own.
	//
	// KubernetesCluster is the worked example: it has no `get` query — DGraph
	// generates one only for a type with an @id field — so it can back a list
	// page and never a detail page. It is nonetheless the honest unit for the
	// list: a second provider type must appear on /clusters without anyone
	// editing a query, which is the whole reason the interface is in the
	// schema.
	//
	// ConfigItem itself is excluded. It is the root every type implements, so a
	// view for it would be "everything" — which is what the list pages already
	// are, one per kind.
	for name, impls := range subInterfaces(types) {
		info, err := c.introspectInterface(ctx, name, impls)
		if err != nil {
			return nil, nil, err
		}
		if _, clash := types[name]; clash {
			// A concrete type and an interface sharing a name is impossible in
			// GraphQL; if it happens the schema is not what we think it is.
			return nil, nil, fmt.Errorf("interface %s collides with a concrete type of the same name", name)
		}
		types[name] = info
	}
	return types, ifaceFields, nil
}

// subInterfaces maps each non-ConfigItem interface to the concrete types
// implementing it, read from what the implementors declare.
func subInterfaces(types map[string]TypeInfo) map[string][]string {
	out := map[string][]string{}
	for name, t := range types {
		for _, in := range t.Implements {
			if in == "ConfigItem" {
				continue
			}
			out[in] = append(out[in], name)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// introspectInterface reads one sub-interface as a view-bearing type.
func (c *DGraphSchemaClient) introspectInterface(ctx context.Context, name string, impls []string) (TypeInfo, error) {
	var obj struct {
		Type *struct {
			Description *string             `json:"description"`
			Fields      []introspectedField `json:"fields"`
		} `json:"__type"`
	}
	if err := c.post(ctx, c.graphqlURL, `{ __type(name: "`+name+`") { description `+fieldSelection+` } }`, &obj); err != nil {
		return TypeInfo{}, fmt.Errorf("introspect interface %s: %w", name, err)
	}
	if obj.Type == nil {
		return TypeInfo{}, fmt.Errorf("interface %s is implemented but cannot be introspected", name)
	}
	doc := ""
	if obj.Type.Description != nil {
		doc = *obj.Type.Description
	}
	return TypeInfo{
		Doc:           doc,
		Fields:        toDerivedFields(obj.Type.Fields),
		IsInterface:   true,
		PossibleTypes: impls,
	}, nil
}

func toDerivedFields(fields []introspectedField) []DerivedField {
	return toDerivedFieldsWithInherited(fields, nil)
}

// toDerivedFieldsWithInherited falls back to an interface's docstring when the
// type's own field carries none. A field's OWN annotation always wins, so an
// implementor can still override what its interface says.
func toDerivedFieldsWithInherited(fields []introspectedField, inherited map[string]string) []DerivedField {
	out := make([]DerivedField, 0, len(fields))
	for _, f := range fields {
		kind, typeName, isList := f.Type.Unwrap()
		doc := ""
		if f.Description != nil {
			doc = *f.Description
		}
		if doc == "" {
			doc = inherited[f.Name]
		}
		out = append(out, DerivedField{
			Name:     f.Name,
			Editable: (kind == "SCALAR" || kind == "ENUM") && !isList,
			Doc:      doc,
			Kind:     kind,
			TypeName: typeName,
			IsList:   isList,
		})
	}
	return out
}

// payloadFieldFor reads the field on `Add<Type>Payload` that carries the
// affected rows — the one whose unwrapped type IS the type itself.
//
// Matching on the TYPE rather than the name is what makes this work without a
// list of exceptions: the payload also carries `numUids`, and the row field is
// named by DGraph's own casing rules, which are irregular enough that they were
// declared by hand for every type until now.
//
// A type with no Add payload (an interface, or one DGraph does not generate
// mutations for) yields "" rather than an error: absence is a normal state and
// the caller falls back.
func (c *DGraphSchemaClient) payloadFieldFor(ctx context.Context, typeName string) (string, error) {
	var out struct {
		Type *struct {
			Fields []introspectedField `json:"fields"`
		} `json:"__type"`
	}
	q := `{ __type(name: "Add` + typeName + `Payload") { fields { name type { kind name ofType { kind name ofType { kind name } } } } } }`
	if err := c.post(ctx, c.graphqlURL, q, &out); err != nil {
		return "", fmt.Errorf("introspect Add%sPayload: %w", typeName, err)
	}
	if out.Type == nil {
		return "", nil
	}
	for _, f := range out.Type.Fields {
		if _, name, _ := f.Type.Unwrap(); name == typeName {
			return f.Name, nil
		}
	}
	return "", nil
}
