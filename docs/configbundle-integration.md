# ConfigBundle Integration Design

This document defines the integration contract between **Orbital** (cloud CMDB) and **ConfigBundle** — the first downstream consumer of Orbital's enrichment pipeline. It is the source of truth for both repositories.

---

## Design Principle

**Orbital is the sole OCI producer.** All consumers — orb, ConfigBundle controller, other teams — pull from Orbital's artifacts. No downstream system needs OCI registry write credentials.

Orbital's publish pipeline supports an optional **enrichment step**: before pushing, Orbital calls configured enrichers to collect additional OCI artifact layers. Each enricher is a stateless HTTP service. Orbital bundles all layers into a single OCI artifact, signs once, and pushes once.

If no enrichers are configured, Orbital behaves exactly as before: raw export only, one push.

---

## Architecture

```
Admin clicks Publish
        │
        ▼
Orbital publish pipeline
        │
        ├── [if enrichers in request body]
        │         │
        │         ├─ POST configbundle-bundler /enrich: {jobId, datacenter}
        │         │         │
        │         │         └─ bundler queries Orbital GraphQL
        │         │             for config fields it needs
        │         │             → returns [{mediaType, data (base64)}]
        │         │
        │         └─ all enrichers must succeed (all-or-nothing)
        │
        ├── bundle layers:
        │     data.json.gz                                   ← orb consumes
        │     schema.gz                                      ← orb consumes
        │     application/vnd.armada.configbundle.manifest.v1+yaml  ← cb-controller
        │
        ├── sign (cosign, once)
        └── push to ACR (once)

Downstream consumers pull from ACR:
  - orb: reads data.json.gz + schema.gz layers (dgraph live import)
  - cb-controller: reads its own layer by media type, applies to cluster
```

---

## Roles

### Orbital

- Calls enrichers synchronously before pushing — **all-or-nothing**: any enricher failure = publish fails, nothing pushed
- Assembles all layers into one OCI manifest, signs once, pushes once
- Records `enriched: true` on the `RegistryArtifact` row when enrichment ran
- Records `enricher_error` when enrichment failed
- Treats enricher layers as opaque bytes identified by media type — no awareness of contents

### ConfigBundle Bundler (enricher)

- Exposes `POST /enrich`
- Receives `{jobId, datacenter}` from Orbital
- Queries Orbital's GraphQL API to fetch the config fields it needs
- Builds the ConfigBundle manifest (YAML or any format)
- Returns `[{mediaType, data}]` where `data` is base64-encoded
- Stateless — no push credentials, no registry access required

### ConfigBundle Controller

- Pulls Orbital's OCI artifact from ACR (read-only credentials only)
- Identifies its layer by media type `application/vnd.armada.configbundle.manifest.v1+yaml`
- Applies the manifest to the cluster (or hands off to GitOps)
- Ignores all other layers (unknown layers are safe to ignore)
- Never pushes to ACR

---

## Enricher API Contract

### Request (Orbital → bundler)

```
POST /enrich
Content-Type: application/json

{
  "jobId": "a1b2c3d4-e5f6-...",
  "datacenter": "colo-galleon"
}
```

`datacenter` matches the `DataCenter.name` field in Orbital's DGraph schema.

### Response (bundler → Orbital)

```json
[
  {
    "mediaType": "application/vnd.armada.configbundle.manifest.v1+yaml",
    "data": "<base64-encoded bytes of your manifest>"
  }
]
```

- `data` is standard base64 (not URL-safe)
- An empty array `[]` is valid — enricher ran but produced no layers
- Non-2xx → Orbital retries up to `ORBITAL_ENRICHER_MAX_ATTEMPTS` times (default 3) with exponential backoff (1s–10s). If all attempts fail, the publish job is marked failed, `enricher_error` is recorded, nothing is pushed to ACR.
- Timeout per attempt (default 30s via `ORBITAL_ENRICHER_TIMEOUT`) → counts as a failed attempt; same retry logic applies.
- Response body exceeding `ORBITAL_ENRICHER_MAX_RESPONSE_BYTES` (default 10 MB) → immediate failure, no retry.

### GraphQL query pattern

The bundler should query Orbital's GraphQL to fetch whatever fields it needs:

```graphql
query ConfigBundleFields($dc: String!) {
  queryDataCenter(filter: { name: { eq: $dc } }) {
    name
    orbId
    # ... whatever config fields cb-controller needs
  }
}
```

Orbital's GraphQL endpoint: `http://orbital/graphql` (or configured `ORBITAL_URL` in the bundler).

**Authentication:** The GraphQL endpoint is currently open (pre-Spike 11). Once Spike 11 Authorization lands, the bundler will need a service account bearer token: `Authorization: Bearer <token>`. This is the bundler's operational concern — Orbital does not issue it.

---

## OCI Artifact Layer Reference

| Layer media type | Producer | Consumer |
|---|---|---|
| `application/vnd.orbital.subgraph.data.v1+gzip` | Orbital (always) | orb — `dgraph live` import |
| `application/vnd.orbital.subgraph.schema.v1+gzip` | Orbital (always) | orb — schema version check |
| `application/vnd.armada.configbundle.manifest.v1+yaml` | ConfigBundle bundler | cb-controller |

Consumers identify their layer by media type. Unknown layers are ignored.

---

## Publish Request (how to trigger)

`POST /api/v1/export/jobs/:jobId/publish` with bundler URL in the body:

```json
{
  "enrichers": [
    "https://configbundle-bundler.internal/enrich"
  ]
}
```

If `enrichers` is omitted or empty, only the raw export layers are pushed (orb-only artifact).

---

## Orbital Configuration

| Variable | Default | Description |
|---|---|---|
| `ORBITAL_ENRICHER_TIMEOUT` | `30s` | Per-attempt HTTP timeout for enricher calls |
| `ORBITAL_ENRICHER_MAX_ATTEMPTS` | `3` | Total attempts (1 initial + 2 retries) |
| `ORBITAL_ENRICHER_MAX_RESPONSE_BYTES` | `10485760` | Max enricher response size (10 MB) |

Enricher URLs are per-request — not configured server-side. The caller supplies them in the publish request body.

---

## Implementation Plan — ConfigBundle Bundler

### What to build

A standalone HTTP service (Go binary recommended for consistency) exposing `POST /enrich`.

### 1. HTTP server

```go
package main

import (
    "encoding/base64"
    "encoding/json"
    "net/http"
)

type enrichRequest struct {
    JobID      string `json:"jobId"`
    Datacenter string `json:"datacenter"`
}

type layer struct {
    MediaType string `json:"mediaType"`
    Data      string `json:"data"` // base64-encoded
}

func handleEnrich(w http.ResponseWriter, r *http.Request) {
    var req enrichRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "bad request", http.StatusBadRequest)
        return
    }

    manifest, err := buildManifest(r.Context(), req.Datacenter)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    layers := []layer{
        {
            MediaType: "application/vnd.armada.configbundle.manifest.v1+yaml",
            Data:      base64.StdEncoding.EncodeToString(manifest),
        },
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(layers)
}
```

### 2. GraphQL query to Orbital

```go
func buildManifest(ctx context.Context, datacenter string) ([]byte, error) {
    // Query Orbital GraphQL for config fields needed for this DC
    // Use ORBITAL_URL env var (default: http://orbital/graphql)
    // Return YAML-encoded manifest bytes
}
```

### 3. Environment variables (bundler)

| Variable | Description |
|---|---|
| `BUNDLER_PORT` | HTTP listen port (default: `8020`) |
| `ORBITAL_GRAPHQL_URL` | Orbital GraphQL endpoint (default: `http://orbital/graphql`) |
| `ORBITAL_BEARER_TOKEN` | Bearer token for Orbital API (empty = no auth; required after Spike 11) |

---

## Local End-to-End Test Flow

This is the recommended flow for testing the full pipeline locally before deploying to AKS.

### Prerequisites

- `make up` running (Orbital stack + MinIO OCI registry at `localhost:5000`)
- `make run-orbital` running (Orbital on `:8001`)
- ConfigBundle bundler running on `:8020`

### Step 1 — Trigger an export

```bash
# Get a datacenter ID from Orbital
curl http://localhost:8001/api/v1/inventory

# Trigger export for a DC
curl -s -X POST http://localhost:8001/api/v1/datacenters/<dcId>/export | jq .
# Note the jobId
```

### Step 2 — Poll until completed

```bash
curl -s http://localhost:8001/api/v1/export/jobs/<jobId> | jq .status
# Wait for "completed"
```

### Step 3 — Publish with enricher

```bash
# Write request body to file (curl best practice for JSON payloads)
cat > /tmp/publish.json <<'EOF'
{
  "enrichers": ["http://localhost:8020/enrich"]
}
EOF

curl -s -X POST \
  http://localhost:8001/api/v1/export/jobs/<jobId>/publish \
  -H "Content-Type: application/json" \
  -d @/tmp/publish.json | jq .
# Note the artifactId
```

### Step 4 — Poll artifact until completed

```bash
curl -s http://localhost:8001/api/v1/oci/artifacts/<artifactId> | jq '{status, enriched, enricherError}'
# Expect: {"status": "completed", "enriched": true, "enricherError": null}
```

### Step 5 — Pull and inspect the artifact

```bash
# List layers in the OCI manifest
oras manifest fetch localhost:5000/orbital/<dc-slug>:v1 | jq '.layers[].mediaType'
# Should include: application/vnd.armada.configbundle.manifest.v1+yaml

# Pull the configbundle layer
oras blob fetch \
  --output /tmp/cb-manifest.yaml \
  localhost:5000/orbital/<dc-slug>@<layer-digest>

cat /tmp/cb-manifest.yaml
```

### Step 6 — Verify the failure path

```bash
# Publish with a bad enricher URL — should fail cleanly
cat > /tmp/publish-bad.json <<'EOF'
{"enrichers": ["http://localhost:19999/does-not-exist"]}
EOF

curl -s -X POST \
  http://localhost:8001/api/v1/export/jobs/<jobId>/publish \
  -H "Content-Type: application/json" \
  -d @/tmp/publish-bad.json | jq .

# Check artifact status
curl -s http://localhost:8001/api/v1/oci/artifacts/<newArtifactId> | jq '{status, enricherError}'
# Expect: {"status": "failed", "enricherError": "enricher failed: ..."}
```

---

## Invariants

- Orbital never imports ConfigBundle packages or code
- Orbital never inspects enricher layer contents — media type and bytes only
- Orbital's raw export layers (`data.json.gz`, `schema.gz`) are always present regardless of enrichment
- No downstream system needs OCI registry write credentials
- Enrichment is all-or-nothing: partial pushes are never produced
- If `enrichers` is omitted in the publish request, behavior is identical to pre-enrichment
- Enricher URLs are per-request (not server-side config); acceptable because the publish API requires Azure AD authn/authz and runs in AKS on VPN. Named server-side enrichers are a future option if governance requirements change.
