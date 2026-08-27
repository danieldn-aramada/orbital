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
	}
}

func (AuditEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("timestamp"),
	}
}

func (AuditEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("resources", AuditEventResource.Type),
		edge.To("resource_types", AuditEventResourceType.Type),
	}
}
