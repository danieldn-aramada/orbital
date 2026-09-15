package schema

import (
	"encoding/json"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AuditEvent is one record in orbital's audit log: an attempt to act on
// orbital's API. Named with the `audit` qualifier deliberately — a bare
// `Event` collides with the several other things this codebase calls events
// (OTel log records, divergence reports, edge Kubernetes events). Kubernetes
// hit the same collision and had to mint an `audit.k8s.io` API group to escape
// it; GitLab compounds the same way (`audit_events`). The external name stays
// `/api/v1/audit-log` — collection vs record, as GitHub words it.
//
// See docs/reference/AUDIT.md § "Naming" for the full rationale.
type AuditEvent struct {
	ent.Schema
}

func (AuditEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.JSON("operations", []string{}).Optional(), // DGraph operation names found in query, e.g. ["updateServer"]
		field.String("actor"),                           // user name or email
		field.Time("timestamp").Default(time.Now),
		field.JSON("details", json.RawMessage{}).Optional(), // {operationName, query, variables, before}
		field.String("event_category").Default("data"),      // "data", "management", or "auth"

		// CloudTrail parity — see docs/reference/AUDIT.md § CloudTrail field parity.
		// All three are Optional: an event written by a background worker has no
		// HTTP request behind it, and NULL is the honest answer. Never default
		// them to "" — an empty string in a column an operator filters on reads
		// as a recorded fact rather than as "not applicable".
		field.String("event_source").Optional(),      // "graphql" | "rest" | "internal"
		field.String("source_ip_address").Optional(), // caller IP (c.RealIP()); empty for internal writers
		field.String("request_id").Optional(),        // X-Request-Id: ties every event from one HTTP request together
	}
}

func (AuditEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("timestamp"),
		// "what else happened in that request" and "what came from that address"
		// are the two questions these columns exist to answer; both are lookups
		// on a growing append-only table.
		index.Fields("request_id"),
		index.Fields("source_ip_address"),
	}
}

func (AuditEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("resources", AuditEventResource.Type),
		edge.To("resource_types", AuditEventResourceType.Type),
	}
}
