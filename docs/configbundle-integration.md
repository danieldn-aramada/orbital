# ConfigBundle Integration Design

This document defines the integration contract between **Orbital** (cloud CMDB), **Orb** (edge service), and **ConfigBundle** — the first downstream consumer of Orbital's enrichment pipeline. It is the source of truth for both repositories.

---

## Design Principle

**Orbital is the sole OCI producer.** No downstream system needs OCI registry write credentials.

**Orb is the single artifact ingress at the edge.** Orb pulls or receives the full multi-layer artifact, decomposes it, imports graph layers to DGraph, and dispatches other layers to registered consumers. ConfigBundle Controller is one such consumer — it receives its manifest layer from orb and applies it. CB Controller does not pull from ACR.

The two pipelines are symmetric:

```
Orbital publish (cloud):         Orb import (edge):
────────────────────────         ──────────────────
DGraph → base export layers      receive full artifact
enrichers ADD layers         ↔   consumers CONSUME layers
sign once, push once             verify, decompose, dispatch
```

---

## Full Architecture

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
        │     data.json.gz                                    ← orb: DGraph import
        │     schema.gz                                       ← orb: DGraph import
        │     application/vnd.armada.configbundle.manifest.v1+yaml  ← orb: dispatched to cb-controller
        │
        ├── sign (cosign, once)
        └── push to ACR (once)
                │
                ▼
            ACR / Zot
                │
                ▼
        Orb import pipeline  (POST /import/artifact)
                │
                ├── cosign verify
                ├── decompose layers by media type
                ├── data.json.gz + schema.gz → DGraph (dgraph live, always)
                └── application/vnd.armada.configbundle.manifest.v1+yaml
                          │
                          └── POST cb-controller /consume  (best-effort dispatch)
                                    │
                                    └── apply manifest to cluster
```

---

## Roles

### Orbital

- Calls enrichers synchronously before pushing — **all-or-nothing**: any enricher failure = publish fails, nothing pushed
- Assembles all layers into one OCI manifest, signs once, pushes once
- Records `enriched: true` on the `RegistryArtifact` row when enrichment ran
- Records `enricher_error` when enrichment failed
- Treats enricher layers as opaque bytes identified by media type — no awareness of contents

### ConfigBundle Bundler (enricher — runs in cloud)

- Exposes `POST /enrich`
- Receives `{jobId, datacenter}` from Orbital
- Queries Orbital's GraphQL API to fetch the config fields it needs
- Builds the ConfigBundle manifest (YAML or any format)
- Returns `[{mediaType, data}]` where `data` is base64-encoded
- Stateless — no push credentials, no registry access required

### Orb (edge)

- Pulls full artifact from registry (OCI source mode) or receives via `POST /import/artifact`
- **Cosign-verifies** the artifact using `ORB_OCI_PUBLIC_KEY_PATH` before decomposing — verification failure rejects the import. CB Controller never sees unverified bytes.
- Decomposes layers by media type
- Always imports `data.json.gz` + `schema.gz` into local DGraph
- Dispatches other layers to registered consumers (`ORB_CONSUMERS` config)
- **Dispatch is best-effort** — DGraph import succeeds regardless of consumer failures
- Records per-consumer dispatch result in the import history entry

### ConfigBundle Controller (consumer — runs at edge)

- **Does not pull from ACR.** Receives its layer from orb's dispatch.
- Exposes `POST /consume` — receives raw manifest bytes from orb
- Applies the manifest to the cluster (or hands off to GitOps)
- Depends on orb being available to receive updates — this is intentional
- Never needs OCI registry credentials

---

## Enricher API Contract (Orbital → bundler)

### Request

```
POST /enrich
Content-Type: application/json

{
  "jobId": "a1b2c3d4-e5f6-...",
  "datacenter": "colo-galleon"
}
```

`datacenter` matches the `DataCenter.name` field in Orbital's DGraph schema.

### Response

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
- Timeout per attempt (default 30s) → counts as a failed attempt
- Response body exceeding `ORBITAL_ENRICHER_MAX_RESPONSE_BYTES` (default 10 MB) → immediate failure, no retry

---

## Consumer API Contract (Orb → cb-controller)

### Request

```
POST /consume
Content-Type: application/vnd.armada.configbundle.manifest.v1+yaml
X-Orb-Tag: v5
X-Orb-Digest: sha256:abc123...
X-Orb-Import-ID: <uuid>

<raw manifest bytes>
```

- Body is the raw layer bytes exactly as the bundler produced them — no base64, no envelope
- `X-Orb-Tag` — the OCI tag that was imported (e.g. `v5`, `latest`)
- `X-Orb-Digest` — the artifact manifest digest
- `X-Orb-Import-ID` — orb's internal import ID for correlation

### Response

- **200** — layer received and accepted for processing
- **4xx / 5xx** — dispatch failed; orb records the error in the import history entry and continues (DGraph import already complete)

Dispatch is **best-effort from orb's perspective**. CB Controller should respond quickly (accept for async processing if needed) rather than block on slow cluster operations.

---

## OCI Artifact Layer Reference

| Layer media type | Producer | Consumer |
|---|---|---|
| `application/vnd.orbital.subgraph.data.v1+gzip` | Orbital (always) | Orb — DGraph live import (built-in) |
| `application/vnd.orbital.subgraph.schema.v1+gzip` | Orbital (always) | Orb — schema apply (built-in) |
| `application/vnd.armada.configbundle.manifest.v1+yaml` | ConfigBundle bundler | CB Controller via orb dispatch |

Layers with no registered consumer are silently ignored — forward compatibility.

---

## Orb Consumer Configuration

```
ORB_CONSUMERS='[{"mediaType":"application/vnd.armada.configbundle.manifest.v1+yaml","url":"http://cb-controller:8080/consume"}]'
```

Port is configurable via `CB_CONTROLLER_PORT` in CB Controller. `:8080` is the conventional default. Multiple consumers:
```
ORB_CONSUMERS='[
  {"mediaType":"application/vnd.armada.configbundle.manifest.v1+yaml","url":"http://cb-controller:8080/consume"},
  {"mediaType":"application/vnd.example.other.v1","url":"http://other-service:9000/consume"}
]'
```

Parsed at orb startup. Dispatch failures do not affect DGraph import.

---

## Publish Request (how to trigger enriched publish)

`POST /api/v1/export/jobs/:jobId/publish` with bundler URL in the body:

```json
{
  "enrichers": [
    "https://configbundle-bundler.internal/enrich"
  ]
}
```

If `enrichers` is omitted or empty, only the raw export layers are pushed (orb-only artifact, no ConfigBundle layer).

---

## Orbital Configuration

| Variable | Default | Description |
|---|---|---|
| `ORBITAL_ENRICHER_TIMEOUT` | `30s` | Per-attempt HTTP timeout for enricher calls |
| `ORBITAL_ENRICHER_MAX_ATTEMPTS` | `3` | Total attempts (1 initial + 2 retries) |
| `ORBITAL_ENRICHER_MAX_RESPONSE_BYTES` | `10485760` | Max enricher response size (10 MB) |

Enricher URLs are per-request — not configured server-side.

---

## Local End-to-End Test Flow

### Prerequisites

- `make up` running (orbital stack + local OCI registry)
- `make run-orbital` running on `:8001`
- `make run-orb` running on `:8010` with `ORB_CONSUMERS` set
- ConfigBundle bundler running on `:8020`
- CB Controller running on `:8030` (exposing `POST /consume`)

### Step 1 — Trigger export + publish with enricher

```bash
# Get a datacenter ID
curl -s http://localhost:8001/api/v1/inventory | jq '.[0].id'

# Trigger export
curl -s -X POST http://localhost:8001/api/v1/datacenters/<dcId>/export | jq .
# Note the jobId. Poll until "completed":
curl -s http://localhost:8001/api/v1/export/jobs/<jobId> | jq .status

# Publish with enricher
cat > /tmp/publish.json <<'EOF'
{"enrichers": ["http://localhost:8020/enrich"]}
EOF
curl -s -X POST http://localhost:8001/api/v1/export/jobs/<jobId>/publish \
  -H "Content-Type: application/json" -d @/tmp/publish.json | jq .
```

### Step 2 — Trigger orb import

```bash
# Via OCI source (if ORB_ENABLE_OCI_REGISTRY=true):
curl -s -X POST http://localhost:8010/api/v1/import \
  -H "Content-Type: application/json" \
  -d '{"tag":"latest"}' | jq .

# Poll import status
curl -s http://localhost:8010/api/v1/import/status | jq .

# Check import history — should show dispatch results per consumer
curl -s http://localhost:8010/api/v1/import/history | jq '.[0]'
```

### Step 3 — Verify CB Controller received its layer

Check CB Controller logs / status — it should have received the manifest bytes via `POST /consume` and applied them.

### Step 4 — Verify failure path

Stop CB Controller, trigger another import. Orb should complete DGraph import successfully. Import history should show CB Controller dispatch as failed with error, graph import as succeeded.

---

## Invariants

- Orbital never imports ConfigBundle packages or code
- Orbital never inspects enricher layer contents — media type and bytes only
- Orbital's raw export layers (`data.json.gz`, `schema.gz`) are always present regardless of enrichment
- No downstream system needs OCI registry write credentials
- Enrichment is all-or-nothing: partial pushes are never produced
- Orb's DGraph import always completes regardless of consumer dispatch results
- CB Controller never pulls from ACR and never needs registry credentials
- Dependency direction: CB Controller is a consumer of orb's dispatch; orb never calls CB Controller proactively
- Unknown layer media types with no registered consumer are silently ignored
- Orb's import history records dispatch receipt (HTTP response from consumer), not apply result. Whether the ConfigBundle CR was applied is CB Controller's observability concern — CR conditions, controller events, controller logs.
- CB Controller apply logic must be idempotent — orb may dispatch the same layer multiple times if imports are re-run
- `ArtifactFetched` and `SignatureVerified` CR conditions are now orb's territory — CB Controller did not fetch or verify. These conditions should be removed or repurposed to reflect what CB Controller actually did. Surfacing `X-Orb-Digest` and `X-Orb-Import-ID` in CR status is recommended for traceability — exact condition design is CB Controller's decision (spike in that repo).
- Same-digest skip optimization (comparing incoming `X-Orb-Digest` to `status.lastAppliedDigest` before running the apply pipeline) is a CB Controller internal concern — not specified here. Design in a CB Controller spike once status condition shape is settled.
