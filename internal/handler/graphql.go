package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	entevent "github.com/armada/orbital/ent/event"
	"github.com/labstack/echo/v4"
)

var mutationOpRe = regexp.MustCompile(`(?i)^\s*mutation\s+(\w+)`)

type mutationConfig struct {
	resourceType string
	getter       string // getX(id: $id) — used when variables contain "id"
	querier      string // queryX(filter: {orbId: {eq: $orbId}}) — used when variables contain "orbId"
}

// mutationRegistry maps GraphQL operation names to resource metadata.
// Add an entry here whenever a new mutable ConfigItem type is introduced.
var mutationRegistry = map[string]mutationConfig{
	"UpdateDataCenter": {resourceType: "DataCenter", getter: "getDataCenter", querier: "queryDataCenter"},
	"UpdateServer":     {resourceType: "Server", getter: "getServer", querier: "queryServer"},
}

// nonEventFields are mutation variable names that carry request metadata, not
// user data. They are excluded from the before/after diff stored in events.
var nonEventFields = map[string]bool{
	"id": true, "ifVersion": true, "version": true,
	"updatedBy": true, "updatedAt": true,
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
// For mutations that include an ifVersion variable, it performs an MVCC check
// against the entity's current version in DGraph before forwarding, and writes
// an audit event on success.
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

	opName := req.OperationName
	if opName == "" {
		if m := mutationOpRe.FindStringSubmatch(req.Query); len(m) > 1 {
			opName = m[1]
		}
	}

	cfg, known := mutationRegistry[opName]
	ifVersion, hasIfVersion := req.Variables["ifVersion"]

	var (
		beforeSnapshot map[string]any
		resourceID     string
		resourceName   string
		actor          string
	)

	if v, ok := req.Variables["updatedBy"]; ok {
		actor, _ = v.(string)
	}

	if known && h.db != nil {
		var before map[string]any
		var fetchErr error
		dataFields := dataVarFields(req.Variables)

		if entityID, _ := req.Variables["id"].(string); entityID != "" {
			before, fetchErr = h.fetchBeforeByID(c.Request().Context(), cfg.getter, entityID, dataFields)
		} else if orbID, _ := req.Variables["orbId"].(string); orbID != "" {
			before, fetchErr = h.fetchBeforeByOrbID(c.Request().Context(), cfg.querier, orbID, dataFields)
		}

		if fetchErr != nil {
			h.logger.Warn("before-fetch failed — skipping event recording", "op", opName, "err", fetchErr)
		} else if before != nil {
			if hasIfVersion && int(toFloat64(before["version"])) != int(toFloat64(ifVersion)) {
				return c.JSON(http.StatusConflict, map[string]string{
					"error": "This record was modified by someone else. Please reload and try again.",
				})
			}
			beforeSnapshot = before
			resourceID, _ = before["orbId"].(string)
			resourceName, _ = before["name"].(string)
		}
	}

	// Strip ifVersion before forwarding — it is not a DGraph field
	if hasIfVersion {
		delete(req.Variables, "ifVersion")
		if modified, err := json.Marshal(req); err == nil {
			bodyBytes = modified
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

	if known && beforeSnapshot != nil && h.db != nil && !hasGQLErrors(respBytes) {
		go h.writeEvent(cfg.resourceType, resourceID, resourceName, actor, beforeSnapshot, dataVarValues(req.Variables))
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

func (h *GraphQL) fetchBeforeByID(_ context.Context, getter, id string, dataFields []string) (map[string]any, error) {
	fields := beforeFieldSet(dataFields)
	query := fmt.Sprintf(`query BeforeFetch($id: ID!) { %s(id: $id) { %s } }`,
		getter, strings.Join(fields, " "))
	body, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"id": id},
	})
	return h.doFetch(getter, body)
}

func (h *GraphQL) fetchBeforeByOrbID(_ context.Context, querier, orbID string, dataFields []string) (map[string]any, error) {
	fields := beforeFieldSet(dataFields)
	query := fmt.Sprintf(`query BeforeFetch($orbId: String!) { %s(filter: { orbId: { eq: $orbId } }) { %s } }`,
		querier, strings.Join(fields, " "))
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
	if entity == nil {
		return nil, fmt.Errorf("entity not found (querier=%s orbId=%s)", querier, orbID)
	}
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

func beforeFieldSet(dataFields []string) []string {
	fieldSet := map[string]bool{"id": true, "orbId": true, "name": true, "version": true}
	for _, f := range dataFields {
		fieldSet[f] = true
	}
	all := make([]string, 0, len(fieldSet))
	for f := range fieldSet {
		all = append(all, f)
	}
	return all
}

func (h *GraphQL) writeEvent(resourceType, resourceID, resourceName, actor string, before, after map[string]any) {
	cleanBefore := make(map[string]any, len(before))
	for k, v := range before {
		if !nonEventFields[k] && k != "orbId" && k != "name" {
			cleanBefore[k] = v
		}
	}

	details, _ := json.Marshal(map[string]any{"before": cleanBefore, "after": after})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.db.Event.Create().
		SetResourceType(resourceType).
		SetResourceID(resourceID).
		SetResourceName(resourceName).
		SetType(entevent.TypeUpdate).
		SetActor(actor).
		SetDetails(json.RawMessage(details)).
		Exec(ctx); err != nil {
		h.logger.Warn("failed to write event", "resource_type", resourceType, "resource_id", resourceID, "err", err)
	}
}

func isMutation(query string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(query)), "mutation")
}

func dataVarFields(vars map[string]any) []string {
	fields := make([]string, 0, len(vars))
	for k := range vars {
		if !nonEventFields[k] {
			fields = append(fields, k)
		}
	}
	return fields
}

func dataVarValues(vars map[string]any) map[string]any {
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		if !nonEventFields[k] {
			out[k] = v
		}
	}
	return out
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
