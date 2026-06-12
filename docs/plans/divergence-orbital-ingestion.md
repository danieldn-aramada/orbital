# Plan — Divergence Reporting (orbital-side ingestion)

> **Scope:** the changes needed in `orbital` (this repo) to ingest divergence snapshots that orb publishes to S3, store them, surface them to the cloud admin, and record resolution decisions.
>
> **Companion:** `divergence-cb-controller-contract.md` (what cb-controller must produce on the configbundle side).
>
> **Pre-reqs settled:** `docs/reference/SDD-CONTEXT.md` §12 (orb-as-relay; on-disk replace-not-merge; orb wraps envelope on publish; manual + cron-scheduled publish; one DC per orb).

## Outcomes when done

1. orbital polls S3 for new divergence snapshots and ingests them into its own store.
2. Each divergence entry is visible on `/divergence` (or `/divergence-reports`) in the orbital UI, scoped per DC.
3. Cloud admin can click **Accept**, **Force**, or **Ignore** per entry; the decision is recorded.
4. **Accept** triggers a GraphQL mutation on orbital intent (admin's intent now matches the override).
5. **Force** records the decision in a new ent table; cb-bundler reads it on next bundle build to emit a `spec.takeover[]` entry.
6. **Ignore** records the decision; entry is tagged in the UI but stays visible until it disappears from snapshots.

## Out of scope

- Ed25519 signing of snapshots and signature verification (post-MVP).
- Manual upload path (Opt 1, courier) — separate follow-up.
- Multi-orb registry; this plan assumes orbital is configured with a static list of `{dc, s3-prefix}`.

## Orb-side prerequisite work (new since D2 decision)

Before the orbital ingestion described below can be meaningful end-to-end, orb gets two pieces of new behavior:

### A. Recognize and store the bundle's `mapping.json` layer

- New media type constant: `application/vnd.armada.configbundle.mapping.v1+json` (defined in configbundle's `bundle/` package; orb imports it)
- In the orb importer (`internal/orb/importer.go`), when iterating bundle layers:
  - Manifest layer → dispatch to consumers (existing flow, unchanged)
  - Graph layers (`data.json.gz`, `schema.gz`) → import to DGraph (existing flow)
  - **Mapping layer** → write to `DataDir/mappings/<bundle-digest>.json`. Do NOT dispatch.
- Pruning: when an old bundle is removed from history, remove its mapping file too.

### B. Translate K8s paths to `orbId`+`field` at intake

The intake handler `POST /api/v1/divergence` changes shape:

**Before (current):**
```json
[
  {"orbId": "...", "field": "...", "intendedValue": ..., "overrideValue": ..., "who": "...", "when": "..."}
]
```

**After (new):**
```json
{
  "bundleDigest": "sha256:abc...",
  "overrides": [
    {"path": "spec.servers[serviceTag=X].idrac.sshEnabled", "intendedValue": false, "overrideValue": true, "who": "local:admin", "when": "..."}
  ]
}
```

Orb's handler:
1. Load `DataDir/mappings/<bundleDigest>.json`. If missing → 422 with `{error: "unknown bundleDigest"}`.
2. Walk each `override.path` against the mapping → produce canonical `{orbId, field, intendedValue, overrideValue, who, when}`.
3. Save the canonical array to `current.json` (existing replace-not-merge).
4. Return 200 with `{stored: N}`.

The PUBLISH side (`POST /api/v1/divergence/publish` and the cron scheduler from this session) stays the same — it reads `current.json` (already canonical), wraps in the envelope, writes to S3.

**This change isolates the translation logic to one place (orb's intake handler) and lets cb-controller stay in pure K8s terms.**

### C. (Optional) Mapping inspection endpoint

`GET /api/v1/mapping/:digest` — returns the stored mapping JSON. Useful for `kubectl exec`-style debugging. Not strictly required for the divergence loop; cheap to add since the file is already on disk.

---

## Components

### 1. New ent entity: `DivergenceEntry`

`ent/schema/divergence_entry.go`

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | |
| `dc_orb_id` | string | identifies the DC (e.g. `colo:colo-galleon`); filterable |
| `entry_orb_id` | string | the ConfigItem orbId the field belongs to (e.g. `colo:srv-001-idrac`) |
| `field` | string | DGraph schema field name (e.g. `sshEnabled`) |
| `intended_value` | JSON | from snapshot |
| `override_value` | JSON | from snapshot |
| `who` | string | (`local:admin` for MVP) |
| `first_seen_at` | timestamp | from `when` on the first snapshot that included this entry — never updated |
| `last_seen_at` | timestamp | from `when` on the most recent snapshot — updated each ingest |
| `last_snapshot_published_at` | timestamp | the `publishedAt` of the snapshot we read this from |
| `created_at` / `updated_at` | timestamps | mixin |

**Indexes:** `(dc_orb_id, entry_orb_id, field)` UNIQUE — one row per "this DC, this orbId, this field." Repeated ingests UPSERT this row.

**Deletion semantics:** when a snapshot for a DC no longer contains a `(orbId, field)` pair that was previously pending, orbital DELETEs the row (or soft-flags as resolved-by-disappearance; pick one — leaning DELETE for simplicity, audit trail comes from `Event` mutation log).

### 2. New ent entity: `DivergenceResolution`

`ent/schema/divergence_resolution.go`

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | |
| `entry_orb_id` | string | matches DivergenceEntry.entry_orb_id |
| `field` | string | matches DivergenceEntry.field |
| `action` | enum | `accept`, `force`, `ignore` |
| `actor` | string | from `actorFromContext` |
| `decided_at` | timestamp | when admin clicked |
| `cb_consumed` | bool | true once cb-bundler has read this and emitted in a bundle |
| `cb_consumed_at` | timestamp | nullable |

**Indexes:** `(entry_orb_id, field)` UNIQUE — one current decision per entry. Re-deciding REPLACES the existing row (or appends? simpler to REPLACE; audit trail in Event log).

**Purpose:**
- `accept`: redundant once intent is mutated (intent change carries the decision forward), but record it so the UI can show "Accepted by X on Y." cb-bundler ignores accept rows.
- `force`: cb-bundler queries un-consumed `force` rows when building a bundle, emits them as `spec.takeover[]`, marks `cb_consumed=true`.
- `ignore`: cb-bundler ignores these rows. The UI tags entries with this resolution.

### 3. Ingestion package: `internal/divergenceingest/`

New package distinct from existing `internal/divergence/` (which is orb-side). Pieces:

- `ingester.go` — S3 client + poller loop
- `poller.go` — `ctrl.Runnable`-equivalent (no controller-runtime here, just a goroutine with context); ticks at configurable interval
- `parser.go` — decodes the snapshot envelope, validates shape
- `store.go` — UPSERT into `DivergenceEntry`, DELETE rows missing from latest snapshot

#### Config (env vars on orbital)

| Env var | Default | Purpose |
|---|---|---|
| `ORBITAL_DIVERGENCE_S3_ENDPOINT` | (same as backup) | S3 endpoint to poll (likely shared with backup S3) |
| `ORBITAL_DIVERGENCE_S3_BUCKET` | (same as backup) | Bucket holding `divergence/<dc>/` prefixes |
| `ORBITAL_DIVERGENCE_S3_ACCESS_KEY` | shared | |
| `ORBITAL_DIVERGENCE_S3_SECRET_KEY` | shared | |
| `ORBITAL_DIVERGENCE_POLL_INTERVAL` | `5m` | How often the poller wakes |
| `ORBITAL_DIVERGENCE_ENABLED` | `false` | Default off; enable explicitly per environment |

For MVP, the list of `{dc, prefix}` to poll is derived from `RegistryArtifact` history — orbital already knows which DCs it has published bundles for, so it polls `divergence/<dc-namespace>/` for each. No new orb registry needed.

#### Poller behavior

1. On tick (or once on startup):
2. List distinct DC namespaces from `RegistryArtifact` table
3. For each DC: `s3.ListObjects` with prefix `divergence/<dc>/`
4. Find the object with the latest `publishedAt` (lexicographic sort on key works because keys are timestamp-prefixed)
5. Skip if this snapshot's `publishedAt` equals the last-ingested `publishedAt` for this DC (idempotent)
6. Otherwise: download, parse the envelope, UPSERT each entry, DELETE entries for this DC not in the envelope
7. Record the new `publishedAt` as last-ingested (small KV — new ent entity `DivergencePollerState{dc, last_ingested_published_at}` OR just track in-memory for now and re-derive on startup from the max `last_snapshot_published_at` per DC)

#### Failure handling

- S3 fetch error → log WARN, skip this tick for this DC, try again next tick
- Parse error → log ERROR with the failing key, alert (in the future); ingestion moves on, this DC won't update until a valid snapshot arrives
- Big drop in entry count (>50% from last ingest) → emit a WARN log only; do not block. Captures the "cb-controller produced a transient empty set" anti-pattern from SDD-CONTEXT §12.8.

### 4. Handler updates: `internal/handler/`

#### `divergence.go` (new — replaces / supersedes the existing stub)

| Method | Route | Behavior |
|---|---|---|
| `List` | `GET /api/v1/divergence` | Returns paged `DivergenceEntry` rows with their current `DivergenceResolution`. Filterable by DC. |
| `Accept` | `POST /api/v1/divergence/:id/accept` | Validates entry; calls existing GraphQL mutation to update intent (read `intendedValue` ← `overrideValue`); records `DivergenceResolution{action: accept}` |
| `Force` | `POST /api/v1/divergence/:id/force` | Records `DivergenceResolution{action: force, cb_consumed: false}` |
| `Ignore` | `POST /api/v1/divergence/:id/ignore` | Records `DivergenceResolution{action: ignore}` |
| `Resolutions` | `GET /api/v1/divergence/resolutions/pending-force` | Queries un-consumed force resolutions. **For cb-bundler to read.** |
| `MarkConsumed` | `POST /api/v1/divergence/resolutions/:id/consumed` | cb-bundler calls this after building a bundle that includes the takeover directive. |

All resolution endpoints require `RoleAdmin` (the "decide what happens to fleet" action is admin-only).

#### `ui.go` — wire `DivergenceReports` to actually render data

Replace the stubbed handler:

```go
func (h *UI) DivergenceReports(c echo.Context) error {
    entries, err := h.db.DivergenceEntry.Query().
        WithResolution().
        Order(ent.Desc(divergenceentry.FieldLastSeenAt)).
        Limit(500).
        All(c.Request().Context())
    // ...
    return h.render(c, "divergence-reports", page.DivergenceReports{
        Base:      h.base(c),
        PageTitle: "Divergence Reports",
        Entries:   entries,
    })
}
```

### 5. UI template work

`web/templates/orbital/pages/divergence-reports.gohtml`:

- DataTable similar to backup/restore — columns: DC, orbId, field, intended, override, who, first seen, last seen, resolution tag
- Per-row buttons: Accept / Force / Ignore (admin only; readonly users see grayed)
- Filters: by DC, by who, by resolution status (all / open / accepted / forced / ignored)
- "Last poll: 14m ago" indicator

HTMX patterns; HX-Request fragments for the row updates after a resolution click.

### 6. Server wiring: `internal/server/server.go`

- Instantiate ingester if `ORBITAL_DIVERGENCE_ENABLED=true`
- Start poller goroutine on startup, cancel on shutdown
- Register handler routes (`/api/v1/divergence/*`)

### 7. Tests

- Unit: parser handles well-formed and malformed envelopes; UPSERT logic; DELETE-stale logic.
- Integration (tagged): poller against a MinIO bucket populated with synthetic snapshots; verify a multi-snapshot run picks the latest, picks empty correctly.
- Handler unit: 200 on resolution actions; 403 for non-admin; 400 for unknown entry ID.

## Migration / rollout

Default `ORBITAL_DIVERGENCE_ENABLED=false`. Land the code; leave disabled in dev-netbox until cb-controller is sending real reports through orb. Flip on per-environment when both sides ready.

## Sequencing within this plan

1. **Orb-side mapping handling (prerequisite A + B above)** — importer recognizes mapping layer; intake handler translates paths to orbId+field. Without this, end-to-end is meaningless.
2. Orbital ent schema migrations (DivergenceEntry, DivergenceResolution) + tests
3. Orbital ingester package + poller, no UI yet
4. Orbital handler endpoints (List, Accept, Force, Ignore, Resolutions)
5. Orbital UI template
6. Wire-up in server.go
7. Integration test against MinIO end-to-end

## Open items deferred

- Snapshot signature verification (post-MVP)
- Multi-orb registration (post-MVP)
- Manual upload path (Opt 1) — `POST /api/v1/divergence/upload`
- Decision history (audit log already covers it via standard `Event` records on resolution actions; if dedicated table is needed later, add `DivergenceResolutionAudit`)
