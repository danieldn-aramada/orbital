# Spike 13 + 17 — Orb Import API & Orb UI: Implementation Plan

**Status:** Planned (Opus design session 2026-05-20)
**Goal:** End-to-end demo — orbital admin exports a data center subgraph, orb pulls it from a local OCI registry (Zot), loads it into local DGraph, and the orb UI lets an operator browse the config and publish a divergence report.

---

## Architecture Summary

```
┌──────────────────────────────────────────────────────────────┐
│  Cloud / AKS                                                 │
│  Orbital → Export DC → Publish signed OCI artifact to ACR   │
└──────────────────────────────┬───────────────────────────────┘
                               │ Zot scheduled mirror from ACR (every 2m)
                               ▼
┌──────────────────────────────────────────────────────────────┐
│  Local (simulates Galleon / colo)                            │
│                                                              │
│  Zot (localhost:5000) ← mirrors from ACR on demand          │
│    ↑ orb polls for new versions                              │
│  Orb (localhost:8010)                                        │
│    → Import Subgraph: pull from Zot, verify, load DGraph     │
│    → Browse config (DC + Servers, read-only)                 │
│    → Divergence: detect drift, publish report to orbital     │
│  DGraph Orb (localhost:8082 / gRPC 9082)                     │
└──────────────────────────────────────────────────────────────┘
```

**Key invariant:** Orb never talks to ACR directly. It only talks to Zot. Zot is the air-gap bridge — it syncs from ACR during a connectivity window; orb imports from Zot's local cache even when disconnected.

---

## OCI Artifact Naming Convention

Defined by `internal/oci/publisher.go` `RepoForDC()`. **Do not change this — orb must match exactly.**

```
<registry>/<repo-prefix>/<dc-slug>:<tag>

ACR:  armadaeksatest.azurecr.io/orbital/<dc-slug>:v1
Zot:  localhost:5000/orbital/<dc-slug>:v1

DC slug: lowercase, spaces→hyphens, non-alphanumeric stripped, trim leading/trailing hyphens
  "Alaska Dot Galleon" → "alaska-dot-galleon"
  "Colo"              → "colo"
  "2F UAE"            → "2f-uae"
```

Layers inside each artifact:
- `data.json.gz` — mediaType `application/vnd.orbital.subgraph.data.v1+gzip`
- `schema.gz`    — mediaType `application/vnd.orbital.subgraph.schema.v1+gzip`

Manifest annotations (read by orb):
- `com.armada.orbital.datacenter-id` — DC orbId
- `com.armada.orbital.export-job-id` — export job UUID
- `org.opencontainers.image.created` — RFC3339 timestamp
- `org.opencontainers.image.version` — tag string
- `com.armada.orbital.cosign-public-key-url` — URL to orbital's public key (ignored if orb has key locally)

---

## Spike 13 — Orb Import API

**Key question:** What is the right mechanism for orb to pull a signed OCI subgraph artifact from a local Zot registry and load it into its local DGraph instance?

### Task 13.1 — Orb config struct (Sonnet)

Create `internal/orbconfig/config.go`. Uses `github.com/kelseyhightower/envconfig` (same as orbital).

```go
type Config struct {
    Port              string        `envconfig:"ORB_PORT"                  default:"8010"`
    DGraphURL         string        `envconfig:"ORB_DGRAPH_URL"            default:"http://localhost:8082/graphql"`
    DGraphAdminURL    string        `envconfig:"ORB_DGRAPH_ADMIN_URL"      default:"http://localhost:8082/admin"`
    DGraphAlphaGRPC   string        `envconfig:"ORB_DGRAPH_ALPHA_GRPC"     default:"localhost:9082"`
    OCIRegistry       string        `envconfig:"ORB_OCI_REGISTRY"          default:"localhost:5000"`
    OCIRepo           string        `envconfig:"ORB_OCI_REPO"              default:"orbital"`
    OCIUsername       string        `envconfig:"ORB_OCI_USERNAME"          default:""`
    OCIPassword       string        `envconfig:"ORB_OCI_PASSWORD"          default:""`
    OCIAllowHTTP      bool          `envconfig:"ORB_OCI_ALLOW_HTTP"        default:"true"`
    OCIPublicKeyPath  string        `envconfig:"ORB_OCI_PUBLIC_KEY_PATH"   default:"cosign.pub"`
    DCSlug            string        `envconfig:"ORB_DC_SLUG"               default:"colo"`
    OrbitalURL        string        `envconfig:"ORB_ORBITAL_URL"           default:"http://localhost:8001"`
    PollInterval      time.Duration `envconfig:"ORB_POLL_INTERVAL"         default:"60s"`
    DataDir           string        `envconfig:"ORB_DATA_DIR"              default:"./orb-data"`
    LogLevel          string        `envconfig:"ORB_LOG_LEVEL"             default:"info"`
}
```

`DataDir` holds import history JSON and downloaded artifact scratch files.

### Task 13.2 — OCI puller (Sonnet)

Create `internal/oci/puller.go`. This is the read-side mirror of `publisher.go`.

Two functions:
```go
// ListTags returns all tags available for this DC's repo in the registry, most recent first.
func ListTags(ctx context.Context, cfg PullConfig) ([]string, error)

// Pull downloads a specific tag's artifact layers and returns the raw bytes.
// Returns dataGZ, schemaGZ, annotations, error.
func Pull(ctx context.Context, cfg PullConfig, tag string) (dataGZ, schemaGZ []byte, annotations map[string]string, err error)
```

`PullConfig` mirrors `oci.Config` but for pulling:
```go
type PullConfig struct {
    Registry  string
    Repo      string
    DCSlug    string
    Username  string
    Password  string
    AllowHTTP bool
}
```

Uses `oras-go/v2` (already a project dependency). Layers are identified by mediaType:
- `application/vnd.orbital.subgraph.data.v1+gzip` → `data.json.gz`
- `application/vnd.orbital.subgraph.schema.v1+gzip` → `schema.gz`

Use `remote.Repository.Tags()` to list. Use `oras.Copy()` with `memory.New()` store to pull, then extract blobs by descriptor mediaType.

**Note:** Zot in local dev uses plain HTTP (`AllowHTTP: true`). The `newRepo()` pattern from `publisher.go` handles this — copy that pattern verbatim.

### Task 13.3 — Cosign verification (Sonnet)

Create `internal/oci/verifier.go`.

```go
// Verify checks the cosign signature on an OCI artifact.
// publicKeyPath: path to cosign.pub (PEM-encoded public key).
// Returns error if signature is invalid or missing.
func Verify(ctx context.Context, repoRef, digestStr, publicKeyPath string, allowHTTP bool) error
```

Use `github.com/sigstore/cosign/v2` (already a project dependency).
Key settings for air-gap safety:
- `TlogUpload: false`
- `IgnoreSCT: true`
- `IgnoreTlog: true`

For the demo, `cosign.pub` is the same key pair used by orbital. The admin deploys orb with this file. Path set via `ORB_OCI_PUBLIC_KEY_PATH`.

**Skip verification if `ORB_OCI_PUBLIC_KEY_PATH` is empty** — allows dev/demo without key setup. Log a warning.

### Task 13.4 — Import pipeline (Sonnet — but read design note first)

> ⚠️ **Design note:** The choice of how to load `data.json.gz` into local DGraph is the central question of Spike 13. The settled approach for this prototype: **`dgraph live` as subprocess**. This matches how orbital's restore works, requires no custom NDJSON parser, and is correct for DGraph's export format. For local demo, the `dgraph` binary is available in the Docker container. Spike 15 (orb deployment model) will revisit this for production.

Create `internal/orb/importer.go`:

```go
type Importer struct {
    cfg    orbconfig.Config
    logger *slog.Logger
}

// Import executes the full import sequence for a pulled artifact.
// 1. drop_all on local DGraph Alpha
// 2. Apply schema.gz to DGraph admin
// 3. Write data.json.gz to DataDir scratch
// 4. Run: dgraph live -f <scratch/data.json.gz> -a <DGraphAlphaGRPC>
// 5. Record import in history file
func (i *Importer) Import(ctx context.Context, dataGZ, schemaGZ []byte, meta ImportMeta) error
```

`ImportMeta` carries tag, digest, DC orbId, export job ID, timestamp — populated from OCI annotations.

Import history is written to `<DataDir>/import-history.json` as a JSON array of `ImportRecord` structs. 25-record rolling window (oldest pruned). This is orb's lightweight persistence — no database needed at prototype stage.

### Task 13.5 — HTTP import endpoint (Sonnet)

Add to orb's Echo server (Task 17.4):

```
POST /api/v1/import          body: {"tag": "latest"} → triggers import job, returns {"jobId": "..."}
GET  /api/v1/import/status   → {"state": "idle|running|done|failed", "currentVersion": "v3", "availableVersion": "v4", "lastImport": {...}}
GET  /api/v1/import/tags     → {"tags": ["v1","v2","v3","latest"]}
GET  /api/v1/import/history  → [{...}, ...]
```

Import runs async in a goroutine. State is held in memory (single import at a time; no job table needed at prototype stage).

### Task 13.6 — Polling loop (Sonnet)

Background goroutine started when orb server starts. Every `ORB_POLL_INTERVAL`:
1. Call `oci.ListTags()` against Zot
2. Compare latest tag against `currentVersion` (loaded at startup from history file)
3. If newer: set `availableVersion` in status — the UI surfaces "New version available"

No auto-import. The operator decides when to import.

### Task 13.7 — Zot config update (Sonnet)

Update `deploy/local/zot-config.json` to add ACR as upstream sync source:

```json
{
  "distSpecVersion": "1.1.1",
  "storage": { "rootDirectory": "/var/lib/registry" },
  "http": { "address": "0.0.0.0", "port": "5000" },
  "log": { "level": "warn" },
  "extensions": {
    "sync": {
      "enable": true,
      "registries": [{
        "urls": ["https://armadaeksatest.azurecr.io"],
        "onDemand": false,
        "tlsVerify": true,
        "credentialsFile": "/etc/zot/credentials.json",
        "content": [{ "prefix": "orbital/**" }],
        "syncInterval": "30s"
      }]
    }
  }
}
```

`onDemand: false` with `syncInterval: "30s"` for local demo — Zot proactively mirrors from ACR on a schedule. Worst-case wait after orbital publishes is 30 seconds, which is acceptable during a live demo. Production deployments would use a longer interval (e.g. `5m` or `15m`) set via a separate `zot-config-prod.json`. Keep `30s` only in `deploy/local/`.

Add `deploy/local/zot-credentials.json` (gitignored, documented in deploy guide):
```json
{
  "armadaeksatest.azurecr.io": {
    "username": "armadaeksatest",
    "password": "<ACR_PASSWORD>"
  }
}
```

Update `deploy/local/docker-compose.yml` to mount credentials file into Zot container.

### Task 13.8 — docker-compose: DGraph for orb (Sonnet)

Add to `deploy/local/docker-compose.yml`:
- `dgraph-orb-zero` — DGraph Zero for orb, ports 5082 (HTTP), 6082 (gRPC)
- `dgraph-orb-alpha` — DGraph Alpha for orb, ports 8082 (HTTP/GraphQL), 9082 (gRPC)

These are entirely separate from orbital's blue/scratch DGraph instances. This matches production: orb has its own local DGraph.

Add Makefile targets:
- `make run-orb` — run orb binary with local defaults
- `make seed-orb-schema` — apply `schema/schema-demo.graphql` to orb's DGraph alpha (empty DB — data comes from import, not seed)

---

## Spike 17 — Orb UI

**Key question:** Can orbital and orb share a Go template infrastructure while serving different navigation, page sets, and capability surfaces? Can the orb UI demonstrate the full end-to-end round-trip?

### Phase 1 — Template infrastructure refactor

> ⚠️ **This is the most disruptive part of the spike.** It touches every handler in orbital. Do it in one focused PR. Validate orbital is visually identical before moving to Phase 2.

#### Task 17.1 — UIConfig struct (Sonnet)

Create `internal/page/ui.go` (alongside existing page data structs):

```go
type UIConfig struct {
    AppName  string    // "Orbital" | "Orb"
    NavItems []NavItem
    BasePath string
    Version  string
    EditMode string    // "intent" (orbital) | "override" (orb)
}

type NavItem struct {
    Label  string
    URL    string
    Icon   string // Font Awesome class e.g. "fa-solid fa-server"
    Active bool
    IsTodo bool  // renders with .todo class → displayTodoToast() on click
}
```

`EditMode` controls how edits are labelled and stored:
- `"intent"` (orbital): edits update authoritative design intent in CCP CMDB. Save button = "Save".
- `"override"` (orb): edits are local overrides tracked against imported intent. Save button = "Override". On save, the original intent value and the new local value are both recorded.

Each orbital handler builds `UIConfig` with `AppName: "Orbital"`, `EditMode: "intent"`. Each orb handler builds with `AppName: "Orb"`, `EditMode: "override"`.

Thread `UIConfig` into every page data struct that currently sets `BasePath` or `Version` directly — consolidate those into `UIConfig`.

#### Task 17.2 — Restructure web/ directory (Sonnet)

**New structure:**
```
web/
  shared/
    static/                      ← move from web/static/ (unchanged)
    templates/
      layouts/
        base.gohtml              ← full page skeleton; {{block "nav" .}} slot
      components/
        toast.gohtml             ← move from web/templates/components/
        todo-toast.gohtml
        hint-banner.gohtml
      pages/                     ← shared config item pages
        datacenter-detail.gohtml ← {{if not .UI.ReadOnly}} guards on edit actions
        server-detail.gohtml     ← same
        server-table.gohtml      ← same
      partials/                  ← shared HTMX fragments
        dc-tabs.gohtml
        server-tabs.gohtml
        (etc.)
  orbital/
    templates/
      components/
        navbar.gohtml            ← orbital's nav (moved from web/templates/components/)
      pages/                     ← all orbital-only pages
        (export, backup, restore, audit, signed-artifacts, schema, divergence, etc.)
      partials/                  ← orbital-specific fragments
  orb/
    templates/
      components/
        navbar.gohtml            ← orb's nav (new)
      pages/                     ← orb-only pages
        status.gohtml
        import-subgraph.gohtml
        import-history.gohtml
        divergence.gohtml
```

**Which pages move to shared vs. stay orbital-only:**

| Template | Goes to | Reason |
|---|---|---|
| datacenter detail + tabs | `shared/templates/pages/` | Both show DC detail; edit mode differs |
| server table + detail | `shared/templates/pages/` | Both show servers; edit mode differs |
| export, backup, restore | `orbital/templates/pages/` | Orbital-only operations |
| audit log | `orbital/templates/pages/` | Intent mutations only on orbital |
| signed artifacts | `orbital/templates/pages/` | Orbital-only |
| schema viewer | `orbital/templates/pages/` | Orbital-only |
| divergence reports | `orbital/templates/pages/` | Orbital surfaces received reports; orb generates them (different page) |
| toast, hint-banner | `shared/templates/components/` | Used by both |
| navbar | each binary's own | Different nav items |

**Edit mode in shared templates.** Orb is NOT read-only — local admins override fields via orb UI, and those overrides become divergences. Both orbital and orb show edit modals; the difference is what happens on save:

- `EditMode: "intent"` (orbital): save calls a GraphQL mutation that updates authoritative intent in CCP CMDB. Button label: "Save".
- `EditMode: "override"` (orb): save calls `POST /api/v1/overrides` on orb. The original intent value and new local value are recorded in `<DataDir>/overrides.json`. DGraph is updated to the local value. Button label: "Override".

In shared templates, use `{{.UI.EditMode}}` to set the button label and the `data-save-mode` attribute. In `app.js`, the save handler checks `window.ORBITAL_CONFIG.editMode` to route to the correct endpoint.

#### Task 17.3 — Update embed directives + template loader (Sonnet)

In `cmd/orbital/main.go` (or wherever `//go:embed` lives today):
```go
//go:embed ../../web/shared ../../web/orbital
var orbitalFS embed.FS
```

In `cmd/orb/main.go`:
```go
//go:embed ../../web/shared ../../web/orb
var orbFS embed.FS
```

Template loader function in `internal/page/loader.go`:
```go
// LoadTemplates parses all .gohtml files from the given FS trees into a single template set.
// Pass the merged FS (shared + binary-specific).
func LoadTemplates(fsys fs.FS, patterns ...string) (*template.Template, error)
```

**Validation gate:** After this task, run `make run-orbital` and manually verify every page renders correctly. Do not proceed to Phase 2 until orbital is confirmed clean.

---

### Phase 2 — Orb web server

#### Task 17.4 — Orb Echo server (Sonnet)

Create `internal/orbserver/server.go` (mirrors `internal/server/server.go`).

Routes:
```
GET  /                    → redirect to /status
GET  /status              → status/dashboard page
GET  /import              → import subgraph page
POST /api/v1/import       → trigger import (proxies to importer)
GET  /api/v1/import/status → import status JSON
GET  /api/v1/import/tags   → available tags from Zot
GET  /api/v1/import/history → import history JSON
GET  /datacenters         → redirect to /datacenter (orb serves one DC)
GET  /datacenter          → DC detail page (shared template, ReadOnly: true)
GET  /servers             → servers page (shared template, ReadOnly: true)
GET  /divergence          → divergence page
POST /api/v1/divergence/publish → publish report to orbital
GET  /import-history      → import history page
POST /api/v1/overrides    → record a local field override {orbId, field, intentValue, localValue}
GET  /api/v1/overrides    → list all current local overrides
GET  /static/*            → static file server (web/shared/static/)
```

No auth for Spike 17. Auth deferred to Spike 16.

Orb's nav items:
- Status (`/status`) — fa-solid fa-satellite-dish
- Data Center (`/datacenter`) — fa-solid fa-building
- Servers (`/servers`) — fa-solid fa-server
- Divergence (`/divergence`) — fa-solid fa-code-branch
- Import History (`/import-history`) — fa-solid fa-clock-rotate-left

#### Task 17.5 — Status/Dashboard page (Sonnet)

Shows:
- DC name + orbId (from loaded config or "No config loaded" state)
- Registry: `localhost:5000/orbital/colo`
- Current version: `v3` (or "—" if nothing imported)
- Last import: timestamp + "3 minutes ago"
- Next poll: countdown or last-checked time
- **New version available:** `v4` — with "Import now" button (if `availableVersion != currentVersion`)
- Connection state (placeholder for now — always "Connected to Zot")

HTMX auto-refresh every 30s via `hx-get="/api/v1/import/status" hx-trigger="every 30s" hx-target="#status-panel"`.

#### Task 17.6 — Import Subgraph page (Sonnet)

Two sections:

**Available versions** — table of tags from `GET /api/v1/import/tags`:
```
Tag     Created         DC               Export Job
v3      2026-05-20...   colo             <uuid>
v2      2026-05-19...   colo             <uuid>
v1      2026-05-18...   colo             <uuid>
```
"Import" button per row. "Import latest" shortcut at top.

**Import progress** — same async polling pattern as orbital's export/backup pages:
- `POST /api/v1/import {"tag": "v3"}`
- Poll `GET /api/v1/import/status` every 2s
- Show steps: Pulling artifact... Verifying signature... Applying schema... Loading data... Done ✓

Signature verification result shown inline (✓ Verified with key fingerprint `abc123` or ⚠️ Skipped — no public key configured).

#### Task 17.7 — DC + Servers pages (Sonnet)

These reuse the shared templates from Task 17.2 with `EditMode: "override"`.

Handler pattern for orb:
```go
func (h *Handler) datacenter(c echo.Context) error {
    // Query orb's local DGraph (same GraphQL as orbital)
    // Build page data with UI: UIConfig{EditMode: "override", ...}
    return render(c, "datacenter-detail.gohtml", data)
}
```

The GraphQL queries are identical to orbital's — same DGraph schema, same query shapes. The difference is which DGraph endpoint is queried (orb's local instance) and how saves are handled.

**Override behaviour in JS:** Base layout sets:
```html
<script>
  window.ORBITAL_CONFIG = { editMode: "{{.UI.EditMode}}", basePath: "{{.UI.BasePath}}" };
</script>
```

In `app.js`, the save handler checks `window.ORBITAL_CONFIG.editMode`:
- `"intent"`: call existing GraphQL mutation endpoint (orbital behaviour, unchanged)
- `"override"`: call `POST /api/v1/overrides` with `{orbId, field, intentValue, localValue}`

Orb also needs a `GET /api/v1/overrides` endpoint so the server detail page can visually mark overridden fields (e.g. a small badge or different field colour indicating "locally overridden").

#### Task 17.8 — Divergence page (Sonnet)

> ⚠️ **Correction from original plan:** Divergence does NOT come from BMC/hardware scanning. Orb is an intent store. Divergence = **local admin overrides** tracked in `<DataDir>/overrides.json`.

Divergence page reads `GET /api/v1/overrides` and displays the full list of locally-overridden fields:

| Type | Name | Field | Intent Value | Local Value | Overridden by | Since |
|---|---|---|---|---|---|---|
| Server | colo-r1-s1 | iDRAC IP | 10.0.1.10 | 10.0.1.99 | local-admin | 14:32 |
| Server | colo-r1-s2 | BIOS profile | performance | balanced | local-admin | 14:35 |

"Publish Report" button → `POST /api/v1/divergence/publish`

Publish sends a structured payload to orbital's report intake API (`ORB_ORBITAL_URL/api/v1/reports`):
```json
{
  "orbId": "<orb-identity>",
  "dcOrbId": "<dc-orbId>",
  "overrides": [
    {"type": "Server", "resourceOrbId": "...", "field": "idracIP", "intentValue": "10.0.1.10", "localValue": "10.0.1.99", "overriddenBy": "local-admin", "overriddenAt": "..."}
  ]
}
```

> ⚠️ **Note:** The report intake API endpoint on orbital doesn't exist yet. For Spike 17, stub it on the orbital side (`POST /api/v1/reports` → 200 + `{"reportId": "<uuid>"}`) so the demo round-trip completes. Full divergence report handling is Spike 14.

#### Task 17.9 — Import History page (Sonnet)

Simple DataTable reading from `GET /api/v1/import/history`:

| Imported | Version | DC | Signature | Status |
|---|---|---|---|---|
| 2026-05-20 14:32 | v3 | colo | ✓ Verified | Done |
| 2026-05-19 09:15 | v2 | colo | ⚠️ Skipped | Done |

No actions — read-only log.

---

## Orb's nav vs. Orbital's nav

| Orbital nav | Orb nav |
|---|---|
| Data Centers | Data Center (singular — this orb's DC) |
| Servers | Servers |
| Export | Import Subgraph |
| Backup | — |
| Restore | — |
| Audit Log | Import History |
| Signed Artifacts | — |
| Schema | — |
| Divergence Reports | Divergence |
| DGraph (todo toast) | — |

---

## Demo Script (End-to-End)

1. **Orbital:** Log in, browse 9 data centers, show servers/iDRAC/storage
2. **Orbital:** Export "Colo" DC → publish to ACR as `v1`
3. **Wait ~30s:** Zot's scheduled mirror picks up `colo:v1` from ACR automatically — no manual step
4. **Orb status page:** Shows "New version available: v1"
5. **Orb import page:** Select v1, click Import — show progress: pulling → verifying → loading
6. **Orb DC page:** Browse imported Colo config — same look as orbital, editable
7. **Orb servers page:** Drill into a server — local admin changes iDRAC IP (hardware swap scenario) → click "Override" → field is marked as locally overridden
8. **Orb divergence page:** Shows the override — "Server colo-r1-s1: iDRAC IP | intent: 10.0.1.10 | local: 10.0.1.99 | overridden by local-admin"
9. **Orb:** Publish divergence report → success
10. **Orbital Divergence Reports:** Report appears — cloud admin can accept / force-override / ignore

---

## Task Order and Dependencies

```
13.7 Zot config update         ← can start immediately
13.8 docker-compose DGraph     ← can start immediately
13.1 Orb config struct         ← can start immediately
13.2 OCI puller                ← needs 13.1
13.3 Cosign verifier           ← needs 13.1
13.4 Import pipeline           ← needs 13.2, 13.3, 13.8
13.5 HTTP import endpoint      ← needs 13.4
13.6 Polling loop              ← needs 13.2, 13.1

17.1 UIConfig struct           ← can start immediately (parallel with spike 13)
17.2 web/ restructure          ← needs 17.1
17.3 embed + template loader   ← needs 17.2 ← VALIDATION GATE: orbital must work before continuing
17.4 Orb Echo server           ← needs 17.3, 13.5
17.5 Status page               ← needs 17.4, 13.5, 13.6
17.6 Import Subgraph page      ← needs 17.4, 13.5
17.7 DC + Servers pages        ← needs 17.3, 17.4
17.8 Divergence page           ← needs 17.4 (orbital stub for intake API)
17.9 Import History page       ← needs 17.4, 13.4
```

**Spike 13 and Spike 17 Phase 1 can run in parallel.** Phase 2 of Spike 17 depends on Spike 13's HTTP endpoints.

---

## Known Implementation Risks

Read this section before starting any task. These are the specific places where Sonnet is likely to get stuck or make a wrong call without explicit guidance.

---

### Risk 1 — `dgraph live` binary availability in local dev (RESOLVE BEFORE STARTING 13.4)

**The problem:** The import pipeline (Task 13.4) execs `dgraph live` as a subprocess. When `make run-orb` runs the orb Go binary on the host, `dgraph` is inside the Docker container — not on the host PATH. The import pipeline will fail at runtime.

**Resolved approach for local dev:** Exec `dgraph live` *inside* the `dgraph-orb-alpha` container using the Docker SDK (`github.com/docker/docker/client`), the same way orbital's restore execs into the `dgraph-live` K8s pod using `client-go`. The pattern is consistent: both use an exec-into-container approach rather than running the binary directly.

For local docker-compose, orb calls:
```
docker exec dgraph-orb-alpha dgraph live -f /tmp/data.json.gz -a localhost:9080
```
Orb writes `data.json.gz` to a volume shared with `dgraph-orb-alpha` before the exec. Add a named volume `orb-import-scratch` to docker-compose, mounted at `/tmp/orb-import` on both the orb container and the dgraph-orb-alpha container.

**Implication:** For local dev, `make run-orb` should run orb as a Docker container (add an `orb` service to docker-compose), not as a bare Go binary. Add `make run-orb` to docker-compose as a service with `build: .` or a pre-built binary mount. This is consistent with how the full local stack is run.

**Note for Sonnet:** Add `github.com/docker/docker/client` as a dependency. The Docker SDK exec pattern is well-documented; use `ContainerExecCreate` + `ContainerExecStart`. Socket is at `unix:///var/run/docker.sock` — mount it into the orb container in docker-compose.

---

### Risk 2 — Cosign verification uses a completely different API than signing

**The problem:** `publisher.go` uses `cosigncli.SignCmd` for signing. The verification path is entirely different. Copying patterns from `publisher.go` will produce broken code.

**Correct verification API:**
```go
import (
    "github.com/sigstore/cosign/v2/pkg/cosign"
    "github.com/sigstore/cosign/v2/pkg/signature"
)

// Load the public key
verifier, err := signature.LoadPublicKeyRaw(publicKeyPEM, crypto.SHA256)

// Build check options
checkOpts := &cosign.CheckOpts{
    SigVerifier: verifier,
    IgnoreTlog:  true,
    IgnoreSCT:   true,
}

// Verify — ref is "<registry>/<repo>/<dc-slug>@<digest>"
sigs, bundleVerified, err := cosign.VerifyImageSignatures(ctx, ref, checkOpts)
```

The `ref` passed to `VerifyImageSignatures` must be a digest reference (not a tag), so orb needs to resolve the tag to a digest during the pull step (Task 13.2) and carry the digest through to verification. The manifest descriptor returned by `oras.Copy` contains the digest — store it in `ImportMeta.Digest`.

**Registry auth for verifier:** If Zot requires auth (it doesn't in local dev with `AllowHTTP: true`), pass a `RegistryClientOpts` into `CheckOpts`. For local dev this is not needed.

---

### Risk 3 — Template naming in merged FS (highest-risk task in the spike)

**The problem:** `template.ParseFS` names each template by the pattern match result. If you parse with glob `"**/*.gohtml"`, the name is the full relative path (e.g. `shared/templates/components/toast.gohtml`). But `{{template "toast" .}}` looks for a template named `"toast"` — not the full path. This will silently produce empty renders with no error.

**Correct approach:** All `{{define "name"}}` blocks use short, unique names (just the semantic name, not a path). The glob pattern passed to `ParseFS` must be written so the resulting template names are predictable and consistent across the merge.

Concretely:
- Every `{{define "..."}}` block keeps its existing short name (e.g. `{{define "navbar"}}`, `{{define "toast"}}`)
- After loading the merged template set, call `t.DefinedTemplates()` and log the result — verify all expected names are present before serving any requests
- If two files both `{{define "navbar"}}`, the second wins silently. Since orbital and orb have separate navbar templates with the same define name, they **must be in separate template sets** (one per binary), not merged into one set. This is why the embed pattern loads `web/shared` + `web/orbital` for orbital and `web/shared` + `web/orb` for orb — the two binary-specific trees never mix.

**Validation gate is hard:** After 17.3, start the orbital server, load every page, and visually confirm no blank sections. Check server logs for template execution errors (`template: X:Y: executing "Z" at <...>: template "name" not defined`). Do not proceed to Phase 2 until this passes.

---

### Risk 4 — `intentValue` must be captured before writing to DGraph

**The problem:** When a local admin overrides a field via the orb UI, orb needs to record both `intentValue` (what orbital said) and `localValue` (what the admin changed it to). If orb reads `intentValue` from DGraph *after* writing the new value, it gets the local value for both — the intent is lost.

**Correct sequence in `POST /api/v1/overrides`:**
1. Read current field value from DGraph → this is `intentValue`
2. Check `overrides.json` — if this field is already overridden, keep the original `intentValue` (don't overwrite it with the current local value)
3. Write new value to DGraph → this is `localValue`
4. Append/update override record in `overrides.json`

`overrides.json` schema:
```json
[
  {
    "resourceType": "Server",
    "resourceOrbId": "server:colo:r1:s1",
    "field": "idracIP",
    "intentValue": "10.0.1.10",
    "localValue": "10.0.1.99",
    "overriddenBy": "local-admin",
    "overriddenAt": "2026-05-20T14:32:00Z"
  }
]
```

On import (Task 13.4), after loading new data into DGraph, **clear `overrides.json`** — the new import replaces orbital's intent, so all previous overrides are resolved. The orb UI should warn the admin if an import will clear existing overrides.

---

### Risk 5 — Template restructure must ship as a standalone PR before any orb work begins

**This is not optional.** Tasks 17.1 + 17.2 + 17.3 must be completed, reviewed, and confirmed working on orbital *before a single line of orb-specific UI code is written.*

Reason: if the template restructure introduces a regression in orbital and orb UI work has already started, it becomes impossible to tell which change caused the break. The restructure touches every handler and every template — it deserves its own focused PR with a clean diff.

**Gate criteria before starting 17.4:**
- `make run-orbital` serves correctly
- Every orbital page renders visually identically to before the restructure
- `t.DefinedTemplates()` output logged at startup lists all expected template names
- No template-related errors in orbital logs under normal navigation

---

## Tasks Requiring Opus Input Before Implementation

Do not proceed without Opus if:
- The Docker exec approach for `dgraph live` (Risk 1) proves unworkable — there may be a simpler loading strategy worth evaluating
- The divergence report schema needs expansion (what fields orbital expects) — relevant when Spike 14 begins
- Auth model for orb's web UI — do not implement any auth without Opus design session (Spike 16)

---

## Open Questions (Deferred to Later Spikes)

| Question | Deferred to |
|---|---|
| Orb web UI auth model | Spike 16 |
| Production orb deployment topology (K8s? standalone?) | Spike 15 |
| Real divergence detection via BMC scan | Spike 15 + 14 |
| Report intake API full implementation on orbital | Spike 14 |
| Orb persistence model (SQLite vs. JSON file vs. other) | Spike 15 |
| Zot scheduled sync vs. on-demand for production | Spike 15 |
| Multi-DC orb (one orb per DC or one orb serving multiple) | Spike 15 |
