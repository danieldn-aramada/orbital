package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/labstack/echo/v4"
)

// knownMutationRe matches any DGraph mutation call on a known ConfigItem type.
// Catches addX, updateX, deleteX for all registered types regardless of operation name.
var knownMutationRe = regexp.MustCompile(`(?i)\b(add|update|delete)(DataCenter|Server|KubernetesCluster|EksaConfig|IPAddress|Rack)\b`)

// orbIdFilterRe extracts orbId values from inline GraphQL filter expressions:
// e.g. filter: { orbId: { eq: "alaska-dot:GRTLY24" } }
var orbIdFilterRe = regexp.MustCompile(`orbId\s*:\s*\{\s*eq\s*:\s*"([^"]+)"`)

var mutationOpRe = regexp.MustCompile(`(?i)^\s*mutation\s+(\w+)`)

// singleEntityTypes maps DGraph getter names to resource type labels, used for
// best-effort resource_id extraction on single-entity mutations.
var singleEntityTypes = map[string]string{
	"UpdateDataCenter":      "DataCenter",
	"UpdateServer":          "Server",
	"UpdateKubernetesCluster": "KubernetesCluster",
	"UpdateEksaConfig":      "EksaConfig",
}

type GraphQL struct {
	dgraphURL string
	db        *ent.Client
	logger    *slog.Logger
}

func NewGraphQL(dgraphURL string, db *ent.Client, logger *slog.Logger) *GraphQL {
	return &GraphQL{dgraphURL: dgraphURL, db: db, logger: logger}
}

type gqlRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// Handle proxies GraphQL requests to DGraph and serves GraphiQL on GET.
// Any mutation touching a known ConfigItem type is recorded as an audit event.
// For single-entity mutations that include ifVersion, an MVCC check is performed.
//
// @Summary     GraphQL endpoint
// @Description POST: proxies GraphQL queries and mutations to DGraph. GET: serves the GraphiQL explorer UI.
// @Tags        graphql
// @Accept      json
// @Produce     json
// @Param       body body string true "GraphQL request body" example("{\"query\": \"{ queryDataCenter { id name } }\"}")
// @Success     200 {object} map[string]interface{}
// @Router      /graphql [post]
func (h *GraphQL) Handle(c echo.Context) error {
	if c.Request().Method != http.MethodPost {
		slog.Info("GET /graphql")
		return c.File("internal/static/index.html")
	}

	bodyBytes, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	var req gqlRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil || !isMutation(req.Query) {
		return h.proxyRaw(c, bodyBytes)
	}

	touchesKnownType := knownMutationRe.MatchString(req.Query)

	opName := req.OperationName
	if opName == "" {
		if m := mutationOpRe.FindStringSubmatch(req.Query); len(m) > 1 {
			opName = m[1]
		}
	}

	var actor string
	if v, ok := req.Variables["updatedBy"]; ok {
		actor, _ = v.(string)
	}
	if actor == "" {
		actor, _ = c.Get("user_name").(string)
	}

	// MVCC check — single-entity mutations only, opt-in via ifVersion variable
	ifVersion, hasIfVersion := req.Variables["ifVersion"]
	if hasIfVersion && h.db != nil {
		resourceType, isSingleEntity := singleEntityTypes[opName]
		if isSingleEntity {
			getter := "get" + resourceType
			entityID, _ := req.Variables["id"].(string)
			orbID, _ := req.Variables["orbId"].(string)

			var before map[string]any
			var fetchErr error
			if entityID != "" {
				before, fetchErr = h.fetchBeforeByID(getter, entityID)
			} else if orbID != "" {
				before, fetchErr = h.fetchBeforeByOrbID("query"+resourceType, orbID)
			}

			if fetchErr != nil {
				h.logger.Warn("before-fetch failed — skipping MVCC", "op", opName, "err", fetchErr)
			} else if before != nil {
				if int(toFloat64(before["version"])) != int(toFloat64(ifVersion)) {
					return c.JSON(http.StatusConflict, map[string]string{
						"error": "This record was modified by someone else. Please reload and try again.",
					})
				}
			}
		}
	}

	// Strip non-DGraph variables before forwarding
	auditOrbID, _ := req.Variables["orbId"].(string)
	needsReMarshal := hasIfVersion || auditOrbID != ""
	if hasIfVersion {
		delete(req.Variables, "ifVersion")
	}
	if auditOrbID != "" {
		delete(req.Variables, "orbId")
	}
	if needsReMarshal {
		if modified, err := json.Marshal(req); err == nil {
			bodyBytes = modified
		}
		// Restore orbId so extractResourceIDs can find it after the DGraph call
		if auditOrbID != "" {
			req.Variables["orbId"] = auditOrbID
		}
	}

	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("proxy to dgraph: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read dgraph response: %w", err)
	}

	if touchesKnownType && h.db != nil && !hasGQLErrors(respBytes) {
		operations, resourceTypes := extractOperations(req.Query)
		resourceIDs := extractResourceIDs(req.Query, req.Variables)
		go h.writeEvent(opName, operations, resourceTypes, resourceIDs, actor, req.Query, req.Variables)
	}

	c.Response().Header().Set("Content-Type", "application/json")
	_, err = c.Response().Writer.Write(respBytes)
	return err
}

func (h *GraphQL) proxyRaw(c echo.Context, body []byte) error {
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("proxy to dgraph: %w", err)
	}
	defer resp.Body.Close()
	c.Response().Header().Set("Content-Type", "application/json")
	_, err = io.Copy(c.Response().Writer, resp.Body)
	return err
}

func (h *GraphQL) fetchBeforeByID(getter, id string) (map[string]any, error) {
	query := fmt.Sprintf(`query BeforeFetch($id: ID!) { %s(id: $id) { id orbId name version } }`, getter)
	body, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"id": id},
	})
	return h.doFetch(getter, body)
}

func (h *GraphQL) fetchBeforeByOrbID(querier, orbID string) (map[string]any, error) {
	query := fmt.Sprintf(`query BeforeFetch($orbId: String!) { %s(filter: { orbId: { eq: $orbId } }) { id orbId name version } }`, querier)
	body, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"orbId": orbID},
	})

	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dgraph fetch: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	data, _ := result["data"].(map[string]any)
	list, _ := data[querier].([]any)
	if len(list) == 0 {
		return nil, fmt.Errorf("entity not found (querier=%s orbId=%s)", querier, orbID)
	}
	entity, _ := list[0].(map[string]any)
	return entity, nil
}

func (h *GraphQL) doFetch(getter string, body []byte) (map[string]any, error) {
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dgraph fetch: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	data, _ := result["data"].(map[string]any)
	entity, _ := data[getter].(map[string]any)
	if entity == nil {
		return nil, fmt.Errorf("entity not found (getter=%s)", getter)
	}
	return entity, nil
}

func (h *GraphQL) writeEvent(opName string, operations, resourceTypes, resourceIDs []string, actor, query string, variables map[string]any) {
	writeAuditEvent(h.db, h.logger, actor, opName, operations, resourceTypes, resourceIDs, map[string]any{
		"operationName": opName,
		"query":         query,
		"variables":     variables,
	})
}

// extractOperations returns deduplicated DGraph operation names and resource type
// names from all mutation calls in the query body.
// e.g. two updateServer calls → operations: ["updateServer"], types: ["Server"]
func extractOperations(query string) (operations []string, resourceTypes []string) {
	matches := knownMutationRe.FindAllStringSubmatch(query, -1)
	seenOp := map[string]bool{}
	seenType := map[string]bool{}
	for _, m := range matches {
		op := strings.ToLower(m[1]) + m[2] // e.g. "update" + "Server" → "updateServer"
		t := m[2]                           // e.g. "Server"
		if !seenOp[op] {
			seenOp[op] = true
			operations = append(operations, op)
		}
		if !seenType[t] {
			seenType[t] = true
			resourceTypes = append(resourceTypes, t)
		}
	}
	return
}

// extractResourceIDs collects orbIds from mutation variables and from inline
// filter expressions in the query body (e.g. filter: { orbId: { eq: "..." } }).
func extractResourceIDs(query string, variables map[string]any) []string {
	seen := map[string]bool{}
	var ids []string

	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	// Variables: single orbId field
	if v, ok := variables["orbId"].(string); ok {
		add(v)
	}

	// Inline filter expressions: orbId: { eq: "..." }
	for _, m := range orbIdFilterRe.FindAllStringSubmatch(query, -1) {
		add(m[1])
	}

	return ids
}

func isMutation(query string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(query)), "mutation")
}

func hasGQLErrors(body []byte) bool {
	var r struct {
		Errors []any `json:"errors"`
	}
	return json.Unmarshal(body, &r) == nil && len(r.Errors) > 0
}

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
