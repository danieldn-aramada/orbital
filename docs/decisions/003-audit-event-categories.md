# 003 — Audit Event Categories

**Status:** Decided

**Date:** 2026-06-02

---

## Decision

Add an `event_category` field to the `events` table. Values: `"data"` (entity mutations) or `"management"` (system-level operations). Default: `"data"`.

The split mirrors AWS CloudTrail's `eventCategory` taxonomy.

---

## Alignment with CloudTrail

| CloudTrail | Orbital |
|---|---|
| `Data` — resource data-plane operations (S3 GetObject, DynamoDB PutItem) | `data` — GraphQL proxy mutations that touch config items (Server, DataCenter, etc.) |
| `Management` — control-plane operations (EC2 CreateInstance, IAM CreateRole) | `management` — system operations: backup trigger, restore, export trigger, schema apply |
| Single unified event stream | Single `events` table — no separate "management trail" |
| Management events surfaced in resource views | Management events included in all entity audit tabs (no orbId filter excludes them) |

---

## Where We Differ from CloudTrail

- **No `Insight` or `ActivityAudit` categories.** Not needed at current scale.
- **No `managementEvent: bool` redundancy.** CloudTrail stores both a category string and a redundant boolean on the same record. We use the string only.
- **orbId-based scoping instead of ARN.** Entity tabs filter by `resource_ids @> '{orbId}'` OR `event_category = 'management'`. There is no ARN hierarchy.
- **No separate trail/delivery infrastructure.** CloudTrail delivers events to S3 or CloudWatch Logs. Orbital's audit log is in PostgreSQL — no delivery pipeline.

---

## Rule: which category to use

| Handler | Category | Rationale |
|---|---|---|
| GraphQL proxy (entity mutations) | `data` | Touches config item data-plane |
| Backup trigger | `management` | System operation, no specific entity |
| Restore completion tombstone | `management` | System operation, no specific entity |
| Export trigger | `management` | System operation, scoped to a DC but is a control-plane op |
| Schema apply | `management` | System operation |

---

## Operation name convention

Management event operation names use `verbNoun` camelCase. The verb describes the operation from the user's perspective, not the implementation mechanism.

| Operation | Name |
|---|---|
| Backup triggered | `createBackup` |
| Restore completed | `restoreBackup` |
| Export triggered | `exportSubgraph` |
| Schema applied | `applySchema` |
| OCI artifact published | `publishArtifact` |

Do not use dot-namespaced names (`dgraph.restore`) or implementation-leaking prefixes (`triggerBackup`). "trigger" is an implementation detail; the audit record names what happened, not how it was initiated.

---

## Why management events surface in all entity tabs

When an administrator views a data center's audit tab, they want to see all events that are relevant context — including system operations like a restore that replaced all data. Filtering only by orbId would hide management events that have no resource ID. The OR clause (`resource_ids @> '{orbId}' OR event_category = 'management'`) ensures management events always appear regardless of which entity tab is open.
