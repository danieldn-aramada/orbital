# Orb Reference

Read this before: orb import pipeline, consumer dispatch, OCI source, divergence intake, DGraphBackend, orb scan, orb UI work.

## Settled Decisions

### Core invariants

- **Orb DGraph is a read-only intent mirror** — orb never mutates DGraph. DGraph retains orbital's authoritative intent verbatim. Orb has no local override mechanism.
- **"Import is sudo"** — `orb import` always runs `drop_all` + live load, overwriting all local DGraph state.
- **Orb divergence transport is not direct HTTP** — orb never sends divergence reports directly to orbital over HTTP. Transport is S3/OCI (deployment layer concern). Direct HTTP between orb and orbital violates the air-gap invariant.
- **Orb does not detect divergence and has no K8s awareness** — `orb start` does not scan hardware, does not read K8s CRs, and is not a K8s controller. Divergence detection is other edge components' responsibility (e.g., ConfigBundle controller reads its own CRs' managedFields). Those components POST reports to orb's intake API; orb publishes to S3/OCI for orbital to consume.
- **Orb is stateless re: DC identity** — `DCSlug` was removed from `orbconfig.Config`. Orb derives which data center it serves from the imported DGraph data (one `DataCenter` node after `drop_all` + live load). `ORB_OCI_REPO` carries the full DC-specific path (e.g. `orbital/colo-galleon`). Do not re-add a `DCSlug` field.
- **Orb UI pages mirror orbital client-side patterns** — same interaction model: GraphQL proxy fetch, DataTables, HTMX tab swap. Not simplified server-rendered alternatives.
- **`orb.spec.ts` is excluded from the default Playwright config** — runs only via `make test-e2e-orb` (`playwright.orb.config.ts`). `playwright.config.ts` has `testIgnore: '**/orb.spec.ts'` to prevent orb tests running against the orbital server on `:8001`.

### Auth

- **Orb has no in-process authentication for MVP** — the HTTP server accepts all requests that can reach the port. Do not add HTTP Basic auth, shared-secret tokens, or any homegrown auth middleware as a stopgap — they do not change the actual threat model and create migration debt that must be thrown away post-MVP.
- **NetworkPolicy is the MVP authZ boundary** — default-deny ingress on orb's namespace; allow rules for cb-controller pods (label-selected), cb-agent pods, and `kube-system` (for `kubectl port-forward`). The NetworkPolicy manifest ships with the orb Helm chart in Spike 15. Until then, orb is open to any in-cluster caller — acceptable because the Galleon management LAN is not internet-reachable and only local admins have access.
- **Local admin access is via `kubectl port-forward` — no orb-level auth** — authorization is fully delegated to kubeconfig RBAC. Orb has no admin user, session, or token concept.
- **Post-MVP: K8s ServiceAccount + TokenReview** — when cb-agent, ConfigBundle controller, courier, and operators need different trust levels, the correct mechanism is projected SA tokens validated via `authentication.k8s.io/v1 TokenReview` scoped per-route. Do not introduce HTTP Basic or an OIDC issuer at the edge — the air-gap invariant prohibits external IdPs.
- **CNI must enforce NetworkPolicy** — AKS with Overlay CNI does. Validate the Galleon CNI choice before relying on NetworkPolicy as the sole gate.

### Import pipeline

- **`POST /api/v1/import/subgraph` is the fast-path import contract** — always registered. Accepts a zip of `data.json.gz` + `schema.gz` only. No consumer dispatch. Used by courier callers and simple integrations.
- **`POST /api/v1/import/artifact` is the full import pipeline** — always registered. Accepts a complete multi-layer OCI artifact zip. Decomposes layers, dispatches non-graph layers to registered consumers, imports graph layers to DGraph. Symmetric reverse of orbital's bundler pipeline.
- **Both import paths must dispatch extra layers** — `triggerImport` (OCI tag pull) and `importArtifact` (direct zip upload) both produce extra layers. Both must run the full consumer dispatch pipeline after DGraph import. Any change to dispatch logic applies to both handlers.
- **Consumer dispatch is best-effort** — graph import always proceeds regardless of consumer dispatch failures. Failures are logged and recorded in the import history entry (per-consumer status + error). Never roll back a DGraph import because a consumer was unreachable.
- **`ORB_CONSUMERS` configures external layer consumers at startup** — JSON array: `[{"mediaType":"...","url":"..."}]`. Dispatch: `POST consumer-url` with raw layer bytes as body, `Content-Type` = media type, `X-Orb-Tag` / `X-Orb-Digest` / `X-Orb-Import-ID` as headers. Unknown media types with no registered consumer are silently ignored.

### OCI source

- **OCI source is a runtime feature activated by `ORB_ENABLE_OCI_REGISTRY=true`** — when enabled: registers `/import/tags` and trigger-by-tag routes; import tags UI visible; OCI pulls feed through `/import/artifact` pipeline. When disabled: only `/import/subgraph`, `/import/artifact`, `/import/history`, `/import/status` available.
- **Cosign verification is required when `ORB_ENABLE_OCI_REGISTRY=true` — no skip path** — `oci.Verify` returns a hard error when `ORB_OCI_PUBLIC_KEY_PATH` is not configured. Do not re-add a skip/warn path. `cosign.pub` lives at `deploy/local/cosign.pub` so local dev is unaffected.
- **Orb is the single artifact ingress at the edge. CB Controller is a registered consumer** — CB Controller exposes `POST /consume`, receives its manifest layer from orb via dispatch, applies it to the cluster. CB Controller does not pull from ACR and does not need registry credentials. Do not re-add OCI pull logic to CB Controller. The dependency direction is: CB Controller is a consumer of orb's dispatch; orb never calls CB Controller directly.

### DGraphBackend

- **`DGraphBackend` interface abstracts orb's dgraph live execution** — `DockerBackend` (default, `ORB_BACKEND=docker`): `docker cp` + `docker exec` into the alpha container. `K8sBackend` (`ORB_BACKEND=k8s`): finds an idle `app.kubernetes.io/name=dgraph-live` pod and execs via SPDY; `ORB_DATA_DIR` must be the shared PVC mount path. Do not collapse the two backends — they serve genuinely different deployment contexts.
- **Orb's K8s backend uses an idle `dgraph-live` pod, not exec into alpha** — reasons: (1) `dgraph live` is a gRPC client, doesn't need to run inside alpha; (2) RBAC: `pods/exec` on a serving pod is a broader attack surface; (3) PVC topology: the shared PVC is mounted on `dgraph-live`, not alpha.
- **`orbserver.New` returns `(*Server, error)`** — K8s backend init failure is a hard startup error, not a warn-and-fallback. Do not change to a panic or silent fallback.

### Import history

- **`ImportMeta.Verified` carries the verification result into the history file** — `Import()` calls `recordHistory(meta, ...)` and `meta.Verified` must be set by the caller before calling `Import()`. Do not add a separate `verified` argument to `recordHistory`.
- **`ImportRecord.Verification` is a three-state string, not a bool** — values: `"verified"` / `"unverified"` / `"not-applicable"`. Constants `VerificationVerified`, `VerificationUnverified`, `VerificationNotApplicable` in `internal/orb/importer.go`. Do not revert to `Verified bool`.
- **`ImportRecord.Layers` replaces `DispatchResults`** — each layer has `Role` (`"graph"/"dispatched"/"unknown"`), `MediaType`, and optional `Dispatch` struct with `StatusCode` and `Error`. Graph layers never have a dispatch record.
- **Import history layer label derivation** — `mediaTypeLabel` template func: strip `application/vnd.`, split by `.`, take second segment. `application/vnd.armada.configbundle.manifest.v1+yaml` → `configbundle`. Used in both summary tags and expanded detail rows.
- **Import history UX conventions** — summary shows per-layer friendly name + ✓/✗ (not aggregate "N dispatched"); expanded detail shows status code badge for HTTP-level failures only (>= 300), nothing for network failures (status 0); dispatch errors in the Error column, not inline in the expanded layer table.

### Divergence

- **Canonical divergence report format** — `{orbId, field, intendedValue, overrideValue, who, when}`. This is the format orb's intake API accepts and orbital displays. ConfigBundle controller must translate SSA field ownership into this format before posting to orb. Field names must match DGraph schema field names.

### orb scan (post-MVP)

- **`orb scan` is a CLI-only subcommand — operator-invoked, stateless, never runs inside the server process** — scans Redfish/iDRAC endpoints, produces GraphQL upsert mutations for human review, and exits. Never auto-imports. Structural enforcement: `internal/orb/` (server package) must never import from `internal/discovery/` or `internal/cli/scan/`.
- **`orb scan` output format: JSON-wrapped GraphQL upsert mutations** — directly pipeable to `curl -d @- orbital:8001/graphql`. Multiple ordered mutations for large scans: IPAddress + Server first, StorageController second, StorageDevice/StorageVolume third.
- **`orb scan` orbId convention for scanned hardware** — `{namespace}:server:{bmc-ip-dashed}` at the server level. Sub-components use Redfish resource IDs. Namespace supplied via `--namespace` flag (required). Do not auto-derive from orb config.
- **`orb scan` only emits orbital-schema fields** — Redfish fields with no schema equivalent are silently dropped. Re-scan updates firmware versions, serial numbers, etc.; leaves admin-set intent fields (`hostname`, `rackPosition`) untouched because they are omitted from scan output.
