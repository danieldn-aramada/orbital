# Getting Started

This is the hands-on onboarding doc. The [README](../README.md) tells you *what* Orbital is; this doc walks you from a fresh clone to running an end-to-end workflow in about 30 minutes.

Skim the [README](../README.md) first if you haven't — the Concepts section establishes the vocabulary used throughout this doc (`orbital`, `orb`, `subgraph export`, `ConfigBundle`, etc.).

---

## Prerequisites

- **Go** 1.25+ — building orbital and orb
- **Docker** + Docker Compose — running the local stack (DGraph, Postgres, Valkey, MinIO, Zot)
- **Make** — orchestration
- **Node.js + npm** (optional) — only if you'll edit CSS

Verify:

```bash
go version       # go 1.25.x or later
docker --version
make --version
```

---

## First-time setup

Run this **once per machine** before anything else:

```bash
make dev-deps
```

This installs a host-side `dgraph` wrapper to `~/.local/bin/dgraph`. Orb's import flow and orbital's restore flow execute `dgraph live` as a subprocess. Since `dgraph` is Linux-only, on macOS the wrapper proxies to a container — without it you'll get `dgraph: executable file not found in $PATH`.

If `~/.local/bin` isn't on your `PATH`, the make output tells you what to add. For most shells:

```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc  # or ~/.bashrc
source ~/.zshrc
```

Verify:

```bash
which dgraph     # should be ~/.local/bin/dgraph
dgraph version
```

---

## Start the local stack

Three terminals. Run these in order.

### Terminal 1 — dependencies

```bash
make up
```

Starts: DGraph (blue + scratch), Postgres, Valkey, MinIO (S3-compatible object store), Zot (OCI registry), and orb's separate DGraph instance.

Wait until you see all containers reach `healthy` / `running`. First run pulls images and takes a couple of minutes.

### Terminal 2 — orbital (cloud)

```bash
make run-orbital
```

Starts the orbital server on port **8001**. Uses `go run` for fast iteration.

### Terminal 3 — orb (edge)

```bash
make run-orb
```

Starts the orb edge service on port **8010**. Uses `go run` for fast iteration.

### Terminal 4 (or just back to one of the above when ready) — seed data

```bash
make seed
```

Loads example DataCenters, Racks, Servers, Clusters, etc. into DGraph, and creates an admin user in Postgres. Run this once after orbital is up; re-run any time you want a clean dataset.

### Verify it worked

Open both UIs side by side:

| App | URL |
|---|---|
| **Orbital** | http://localhost:8001 |
| **Orb** | http://localhost:8010 |
| Orbital GraphQL playground | http://localhost:8001/graphql |
| Orbital Swagger UI | http://localhost:8001/swagger/index.html |
| Orb Swagger UI | http://localhost:8010/swagger/index.html |

Both pages should load. If either returns a 500 or doesn't connect, see [Common issues](#common-issues) below.

---

## Log in

Orbital uses session-based auth in local dev (no OIDC required locally).

| Credential | Value |
|---|---|
| Email | `admin@armada.ai` |
| Password | `admin` |
| Role | `admin` |

A read-only user is also seeded:

| Credential | Value |
|---|---|
| Email | `user@armada.ai` |
| Password | `user` |
| Role | `readonly` |

Log in as admin for this walkthrough so you can perform mutations.

---

## Take a quick tour

Spend ~5 minutes clicking around before the workflow walkthrough. The mental model lands faster than reading about it.

| Where | What to look at |
|---|---|
| **Inventory** | List of all seeded Servers. Try clicking a row to drill into a Server detail page. |
| **DataCenter detail** (click a DC from the menu) | The graph-relationship view: a DC has Racks, Racks have Servers, Servers have iDRAC settings and Storage. Each is a separate sub-tab. |
| **Clusters** | KubernetesCluster catalog with management/workload tree view. |
| **Audit log** | Every mutation is recorded with actor, timestamp, and a before/after diff. This is core to the intent-store story. |
| **Backups** | DGraph backup history; trigger a backup with the *Backup Now* button. |
| **Export Subgraph** | The publish pipeline: scope an export to one DC and emit a signed OCI artifact. You'll use this in the workflow below. |

Now switch to orb (http://localhost:8010):

| Where | What to look at |
|---|---|
| **Inventory / DataCenter / Cluster** | Same data shape as orbital, but with no Edit / Delete buttons — orb is read-only. |
| **Import** | The OCI artifact intake page. You'll use this in the workflow below. |
| **Divergence** | Where local-admin overrides surface (empty for now — we'll get there in a deeper walkthrough). |

---

## End-to-end workflow: edit intent, export, import, verify

This is the canonical flow. ~10 minutes. Demonstrates the whole product loop: cloud-authored intent → signed OCI artifact → edge import → edge-served read-only data.

### Step 1 — Edit a server in orbital

1. In the orbital UI (http://localhost:8001), navigate to **Inventory**.
2. Pick a seeded server in the `seattle` namespace — say `seattle:JD268Y3` (a `PowerEdge R350`).
3. Click into the server detail page.
4. Click **Edit**.
5. Change the **model** field from `PowerEdge R350` to `PowerEdge R350-DEMO`.
6. Click **Save**.

The modal closes and the new model value appears on the page.

### Step 2 — Verify the audit log

1. Still on the server detail page, click the **Audit Log** sub-tab.
2. The top entry should be a `updateServer` mutation with:
   - Actor: `admin@armada.ai`
   - Timestamp: just now
   - A before/after diff showing `model: PowerEdge R350 → PowerEdge R350-DEMO`

If the audit entry doesn't appear, the mutation didn't take — check the browser console for errors, and check terminal 2 (`run-orbital`) for handler logs.

### Step 3 — Export the subgraph

1. In orbital, navigate to **Export Subgraph** (or `/export`).
2. The Data Center select should show options from seeded data — pick **`seattle:seattle-galleon`**.
3. Click **Export**.
4. A job appears in the Export Jobs table with status `pending` → `running` → `completed`. This usually takes 5–10 seconds.

What happened under the hood:
- Orbital ran a scoped query against DGraph for the seattle namespace
- Wrote `data.json.gz` (the graph data) + `schema.gz` (the GraphQL schema) to a scratch DGraph instance
- Exported them as an OCI artifact
- Pushed the signed artifact to the local Zot registry (default at `localhost:5001`)

### Step 4 — Verify the artifact landed in the registry

In a new terminal:

```bash
# List tags in the local OCI registry
curl -s http://localhost:5001/v2/seattle/tags/list | jq
```

You should see a tag like `v1` (or higher if you've published before). The artifact contains the export from Step 3, signed with cosign.

### Step 5 — Import into orb

1. Switch to the orb UI (http://localhost:8010).
2. Navigate to **Import**.
3. The Tags table should show the tag(s) from Step 4.
4. Find the newest tag and click **Import**.
5. Wait for the status to reach `done` (typically 5–15 seconds). The import-history table updates with the result.

What happened under the hood:
- Orb pulled the OCI artifact from the local Zot registry
- Verified the cosign signature
- Loaded the artifact's `data.json.gz` + `schema.gz` into orb's local DGraph via `dgraph live`
- (If configured) dispatched additional layers to consumers like cb-controller — there are no extra layers in this basic export, so this step is a no-op

### Step 6 — Verify the change appears in orb

1. In the orb UI, navigate to **Inventory**.
2. Find `seattle:JD268Y3` again.
3. The **model** field should now read `PowerEdge R350-DEMO`.

You just observed the full intent-to-edge loop: edit in orbital → export → transport via OCI registry → import into orb → serve at the edge.

### Step 7 (optional) — Tour the divergence loop

The next layer of the product is divergence: what happens when local admins override config at the edge, and how orbital resolves it. That flow involves cb-controller and is more elaborate — see [`docs/reference/DIVERGENCE.md`](reference/DIVERGENCE.md) and the `make e2e-divergence` script for the full loop.

---

## Common issues

### `dgraph: executable file not found in $PATH`

You missed `make dev-deps`, or `~/.local/bin` isn't on your `PATH`. Run `make dev-deps`, then ensure `~/.local/bin` is on PATH:

```bash
echo $PATH | tr ':' '\n' | grep .local/bin   # should print a line
which dgraph                                  # should print ~/.local/bin/dgraph
```

### Ports already in use

`make up` will fail if ports 5432 (Postgres), 6379 (Valkey), 8080/9080 (DGraph blue), 8081/9081 (DGraph scratch), 8082/9082 (orb DGraph), 5001 (Zot), 9000/9001 (MinIO) are taken.

Find and kill the conflicting process:

```bash
lsof -ti :8001 | xargs kill -9    # adjust port number
```

### Orbital won't start (port 8001 in use)

A previous `make run-orbital` may have zombie listeners:

```bash
lsof -ti :8001 | xargs kill -9
make run-orbital
```

### Orb won't start (port 8010 in use)

Same pattern:

```bash
lsof -ti :8010 | xargs kill -9
make run-orb
```

### Seed fails with "no such table" or schema mismatch

The Postgres schema didn't initialize. Re-run `make down && make up && make seed`.

### Import says "no tags available" in orb

You haven't published yet. Run an Export in orbital first (Step 3 above), then refresh the orb Import page.

### The audit log shows no entries

Make sure you logged in as `admin@armada.ai` (not `user@armada.ai` — readonly users can't mutate). Check terminal 2 for handler logs.

---

## Tearing down

When you're done for the day:

```bash
# Stop orbital and orb (Ctrl+C in their terminals — or...)
lsof -ti :8001 | xargs kill -9
lsof -ti :8010 | xargs kill -9

# Stop the local stack and wipe its volumes
make down
```

`make down` wipes the volumes — DGraph, Postgres, MinIO, and Zot all start empty next time. Re-run `make seed` after the next `make up` to restore seeded data.

---

## Where to go next

| If you want to... | Read |
|---|---|
| Understand the architecture in detail | [README — Reference Architecture](../README.md#reference-architecture) and [`docs/orbital-arch.png`](orbital-arch.png) |
| Contribute code | [`CONTRIBUTING.md`](../CONTRIBUTING.md) |
| Understand the divergence model | [`docs/reference/DIVERGENCE.md`](reference/DIVERGENCE.md) |
| Work on UI / HTMX / templates | [`docs/reference/UI.md`](reference/UI.md) |
| Edit the DGraph schema | [`docs/reference/DGRAPH.md`](reference/DGRAPH.md) |
| Set up auth / OIDC | [`docs/auth.md`](auth.md) |
| Deploy to AKS | [`deploy/README.md`](../deploy/README.md) |
| See the roadmap and current state | [`ROADMAP.md`](../ROADMAP.md) |
| Use the CLI (`orbctl`) | [README — API Access](../README.md#api-access) |

If you got stuck somewhere this doc didn't anticipate, that's a doc bug — flag it in your next PR.
