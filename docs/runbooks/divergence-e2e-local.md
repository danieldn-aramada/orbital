# Divergence pipeline e2e — local runbook & gotchas

Step-by-step guide for running the full divergence flow locally (orbital → cb-bundler → orb → cb-controller → divergence reporter → orbital ingest → accept → DGraph mutation). Captures every gotcha that wasted time in past sessions so the same mistakes aren't repeated.

## Service inventory and ports

| Service | Port | Repo | How to start |
|---|---|---|---|
| orbital | 8001 | `~/armada/orbital` | `make run-orbital` (with env, see below) |
| orb | 8010 | `~/armada/orbital` | `make run-orb` (with env, see below) |
| cb-bundler | 8020 | `~/armada/configbundle` | `make run-bundler` |
| cb-controller | 8095 | `~/armada/configbundle` | `make run-controller` (with env, see below) |
| zot OCI registry | 5001 | docker compose | `make up` |
| MinIO (S3) | 9000 (API) / 9001 (UI) | docker compose | `make up` |
| orbital DGraph | 8080 (GraphQL) / 9080 (gRPC) | docker compose | `make up` |
| orb DGraph | 8082 (GraphQL) / 9082 (gRPC) | docker compose | `make up` |

## Required env on each binary

**orbital** — for local e2e where the bundler is plain HTTP and curl over HTTP needs to send cookies:
```bash
ORBITAL_OIDC_ISSUER_URL=""              # disable bearer auth so bundler plain-HTTP queries succeed
ORBITAL_COOKIE_SECURE=false             # session cookie usable over HTTP curl
ORBITAL_BUNDLER_URLS="http://localhost:8020/bundle"   # WITHOUT THIS, PUBLISH SKIPS BUNDLER — artifact ends up with only dgraph layers, cb-controller sees nothing to consume
PATH="$HOME/.local/bin:$PATH"           # so dgraph wrapper resolves (for restore path; not strictly needed for orbital publish)
```

**orb** — pointing consumers at cb-controller:
```bash
ORB_CONSUMERS='[{"name":"cb-controller","url":"http://localhost:8095/dispatch"}]'
PATH="$HOME/.local/bin:$PATH"           # dgraph wrapper required for the `dgraph live` import subprocess
```
Note: `ORB_CONSUMERS` uses the consumer-centric shape (name + single dispatch URL). cb-controller's `/dispatch` accepts the layer and routes internally by media type. Post-ADR-011 the bundler emits ONE layer (manifest.v1+yaml); the old two-endpoint shape (`/consume` + `/mapping`) is dead.

**cb-controller** — connecting to orb's divergence intake with a fast reporter interval for testing:
```bash
NAMESPACE=default
CB_CONTROLLER_PORT=:8095
ORB_DIVERGENCE_INTAKE_URL=http://localhost:8010/api/v1/divergence
DIVERGENCE_REPORTER_ENABLED=true
DIVERGENCE_REPORTER_INTERVAL=10s        # default is 5min — too slow for interactive testing
```

**cb-bundler** — no env required; defaults to `ORBITAL_GRAPHQL_URL=http://localhost:8001/graphql` and warns `ORBITAL_OIDC_CLIENT_SECRET not set — using plain HTTP`. The warning is fine if orbital has OIDC disabled.

## Auth gotchas

- **Local admin credentials**: `admin@armada.ai` / `admin` (from `scripts/seed.sh`). NOT `admin@orbital.local`.
- **OIDC bearer auth blocks the bundler**: orbital's default config has `ORBITAL_OIDC_ISSUER_URL` set to the Azure AD tenant URL. With that set, the GraphQL endpoint requires a bearer token. cb-bundler doesn't have one in local dev (uses plain HTTP). Disable orbital OIDC for local e2e by setting `ORBITAL_OIDC_ISSUER_URL=""`.
- **Cookie Secure attribute breaks curl over HTTP**: orbital defaults to `ORBITAL_COOKIE_SECURE=true`. curl won't send a Secure cookie over HTTP. Set `ORBITAL_COOKIE_SECURE=false` for local curl-based testing.
- **CSRF token is HTML-entity-encoded in the page**: when you scrape `name="csrf" value="..."` from `GET /`, the `+` character comes back as `&#43;`. Use `html.unescape()` in Python before sending the token in the login POST.
- **`RequireRole` middleware still enforces with OIDC disabled**: disabling OIDC doesn't bypass role gating on POST/PUT/DELETE under `/api/v1/`. You still need a logged-in admin session for mutations. GETs pass through unconditionally.

## API gotchas

- **Export jobs use `jobId`, not `id`**: `GET /api/v1/export/jobs` returns `[{"jobId": "...", ...}]`. Parsing `j["id"]` fails. This differs from `GET /api/v1/oci/artifacts` which DOES use `id`.
- **Publish handler doesn't read `tag` from body**: it computes the next tag via `nextTagForJob`. Sending `{"tag":"v2"}` is ignored. Just send `{}`.
- **Empty `$JOB_ID` produces `/api/v1/export/jobs//publish` (double slash)** → 400. If you see this URL in logs, your shell variable was empty. Bash variables don't persist across separate Bash tool calls — inline the value or do the whole flow in one Bash call.

## Bundler gotchas

- **If orbital is published without `ORBITAL_BUNDLER_URLS` set, the artifact has only 2 layers (dgraph data + schema), no manifest.** cb-controller's `/dispatch` is never called → no ConfigBundle CR appears → SSA test can't proceed. Always check the artifact's layer count after publish (expected: 3 — dgraph data, dgraph schema, configbundle manifest). Post-ADR-011 (2026-06-16) the bundler emits ONE layer only — orbIds are saturated directly on the ConfigBundle CR spec so no separate mapping layer is needed. If you see 4 layers, you're running a pre-ADR-011 cb-bundler image.
- **Old artifacts in the registry may have been built before orbId emission**: if the CRD requires `orbId` as listMapKey (current state per `~/armada/configbundle/docs/plans/server-identity-orbid.md`) but you import an artifact built by the old bundler, cb-controller's apply errors with `duplicate entries for key [orbId=""]` (one per server). Always publish a FRESH artifact tag with the current bundler before testing — don't reuse old tags like `v1` that were produced by previous bundler versions.

## ConfigBundle/SSA gotchas

- **listMapKey is `orbId`** (as of the server-identity-orbid migration; landed in configbundle main). SSA payloads must identify the target server by `orbId: colo:<serviceTag>`, NOT by `serviceTag: <serviceTag>`. Confirm at any time with `kubectl get crd configbundles.armada.ai -o yaml | grep listMapKey` — should print `x-kubernetes-list-map-keys: [orbId]`. Example correct payload:
  ```yaml
  spec:
    servers:
      - orbId: colo:JQK3V64       # ← listMapKey identity, not serviceTag
        idrac:
          sshEnabled: true
  ```
  Wrong key produces a kubectl error like `element 0: associative list with keys has an element that omits all key fields ["orbId"]`.
- **`--force-conflicts` is required for admin overrides**: configbundle-controller already owns every field; the local override has to explicitly take ownership. Without `--force-conflicts`, you get "conflict" errors.
- **Field manager naming convention is `local:<admin-id>`** (e.g. `local:daniel`). The divergence reporter watches for any field manager starting with `local:` to identify operator overrides. Other prefixes are ignored.
- **Hostname is the K8s resource name, orbId is the spec key**: a server can be referenced by `metadata.name: r09-u22.colo-galleon` (lowercased hostname) AND by `spec.servers[*].orbId: colo:JQK3V64`. They serve different purposes; don't confuse them. serviceTag is also stored on the server (`spec.servers[*].serviceTag`) but is no longer the listMapKey.

## Divergence reporter gotchas

- **Default reporter interval is 5min** — far too slow for interactive testing. Always set `DIVERGENCE_REPORTER_INTERVAL=10s` in dev.
- **Reporter needs a valid `lastManifest` to compare against**: it computes "intent" from what was last consumed via `/consume`. If you SSA before the first import completes, there's nothing to compare against and the override gets ignored.

## Orbital ingest gotchas

- **Ingester polls S3 every `ORBITAL_DIVERGENCE_POLL_INTERVAL`** (default 10s). After publishing the divergence to S3 via `POST /api/v1/divergence/publish` on orb, allow up to 10s for orbital to ingest.
- **MinIO is `s3:9000` from inside Docker compose, `localhost:9000` from host**: divergence config in orb and orbital both default to `localhost:9000` for local dev. Don't override unless deploying.
- **Empty divergence publishes are rejected (409)**: orb refuses `POST /api/v1/divergence/publish` when its store has zero entries. If you see 409 after a fresh start, you forgot to seed (or the cb-controller divergence reporter hasn't ticked yet).

## Acceptance flow gotchas

- **Accept dispatches `update{Type}` GraphQL mutation back to DGraph via orbital's GraphQL proxy.** Without the proxy (or with `ORBITAL_OIDC_ISSUER_URL` set but no token), the accept handler errors. The accept code path is orbital-internal — `actor` comes from the logged-in admin session.
- **MVCC `ifVersion` check** can reject the mutation if the DGraph node's `version` field changed since the divergence was captured. For local testing, this is rare but possible if you re-trigger imports between divergence capture and accept.
- **Each accept "uses up" the target server for the next test cycle.** After accept, orbital's DGraph state matches what local:admin overrode to. The bundler's next publish then emits that value as the new "intent." If you SSA the SAME field on the SAME server back to the SAME value, the reporter correctly sees `intended == override` and reports 0 divergences — looks like a bug, isn't one. For repeated testing on the same server, either:
  - Pick a **fresh untouched server** each cycle (query DGraph for one with the original value: `curl http://localhost:8080/graphql -d '{"query":"{ queryDataCenter(filter:{orbId:{eq:\"colo:colo-galleon\"}}) { servers(first:10) { orbId hostname idracSettings { sshEnabled } } } }"}'`)
  - Or revert the DGraph state via a direct mutation before the next test cycle: `mutation { updateIdracSettings(filter:{orbId:{eq:"colo:JQK3V64-idrac"}}, set:{sshEnabled:false}) { numUids } }`
  - Or test a different field (e.g. `ipmiEnabled` instead of `sshEnabled`) — each field tracks its own intent independently.

## State cleanup between runs

For a fully fresh state:

```bash
# 1. Wipe orb-data (import history + divergence store + override state)
rm -rf ~/armada/orbital/orb-data/

# 2. Delete the ConfigBundle CR (cascades to ServerConfig children via ownerRefs)
kubectl delete configbundle colo-galleon -n default --ignore-not-found

# 3. Clear MinIO divergence/ prefix (orphaned snapshot files)
docker run --rm --network=local_default --entrypoint=sh minio/mc:RELEASE.2025-08-13T08-35-41Z -c '
  mc alias set local http://minio:9000 minioadmin minioadmin > /dev/null
  mc rm --recursive --force local/orbital/divergence/ 2>/dev/null
'

# 4. (Optional) Reset orb's DGraph data — drops all imported subgraph data
docker compose -f deploy/local/docker-compose.yml restart dgraph-orb-alpha dgraph-orb-zero
```

DGraph in orbital itself is the source of truth — don't wipe it unless re-seeding.

## Bugs found during 2026-06-13 e2e run (fix before next test)

1. **cb-controller `extractAdminPaths` hardcodes `local:admin`** — `internal/controller/divergence_reporter.go` literally checks `entry.Manager != "local:admin"`. Documented behavior is "any field manager starting with `local:`" but only the exact string is accepted. Any other suffix (`local:daniel`, `local:ops`) is silently filtered out and the divergence is never reported. **Fix in configbundle**: change to `!strings.HasPrefix(entry.Manager, "local:")`. **Workaround until fixed**: use `--field-manager=local:admin` exactly.

2. **orbital ingester used full registry-prefixed Repository as S3 prefix (FIXED in this session)** — `internal/divergenceingest/ingester.go::discoverDCs` was passing `r.Repository` directly (`localhost:5001/orbital/colo-galleon`) into `divergence/<repo>/` for S3 listing. Orb publishes to `divergence/orbital/colo-galleon/` (no registry host — see `internal/divergence/divergence.go`). The two prefixes never matched and the ingester silently saw zero keys per tick. Fix: strip the leading registry segment before building the prefix. Verified working — ingester now reports `applied snapshot ... entries: 1` on poll.

## Process management

- **`go run` from a non-interactive shell doesn't have `~/.local/bin` on PATH by default** even when `.zshrc` adds it. Always prepend `PATH="$HOME/.local/bin:$PATH"` when starting orb (it needs the dgraph wrapper).
- **Kill ports cleanly between restarts**: `lsof -ti :8001 :8010 :8020 :8095 | xargs kill -9 2>/dev/null` followed by `sleep 2` before restarting. Otherwise the new process fails with `bind: address already in use`.
- **`go run` processes show up as `go run` in `ps`, not the binary name**: `pkill go-build` doesn't work. Use port-based kill instead.

## Full sequence for a clean e2e

```bash
# 1. Start dependencies
cd ~/armada/orbital && make up

# 2. Start orbital with all required env (terminal 1)
ORBITAL_OIDC_ISSUER_URL="" \
ORBITAL_COOKIE_SECURE=false \
ORBITAL_BUNDLER_URLS="http://localhost:8020/bundle" \
PATH="$HOME/.local/bin:$PATH" \
  make run-orbital

# 3. Start orb with consumer URLs (terminal 2)
ORB_CONSUMERS='[{"name":"cb-controller","url":"http://localhost:8095/dispatch"}]' \
PATH="$HOME/.local/bin:$PATH" \
  make run-orb

# 4. Start cb-bundler (terminal 3)
cd ~/armada/configbundle && make run-bundler

# 5. Start cb-controller with fast reporter (terminal 4)
cd ~/armada/configbundle && \
  NAMESPACE=default \
  CB_CONTROLLER_PORT=:8095 \
  ORB_DIVERGENCE_INTAKE_URL=http://localhost:8010/api/v1/divergence \
  DIVERGENCE_REPORTER_ENABLED=true \
  DIVERGENCE_REPORTER_INTERVAL=10s \
  make run-controller

# 6. Login (admin@armada.ai / admin) and curl through the flow.
#    Response field for the job/artifact identifier is `id` everywhere (not jobId):
#    - POST /api/v1/export {"orbId":"colo:colo-galleon"} → returns {"id":"<uuid>", ...}
#    - Wait for status=completed
#    - POST /api/v1/export/jobs/<id>/publish {} → returns {"artifactId":N, "tag":"vN", ...}
#    - Wait for artifact status=completed (4 layers — dgraph data, dgraph schema, manifest, mapping)
#    - POST orb's /api/v1/import {"tag":"vN"}
#    - Wait for orb import status=done, CR appears in K8s with 49 servers
#    - kubectl apply --server-side --force-conflicts --field-manager=local:admin
#      using orbId (e.g. orbId: colo:<serviceTag>) as the listMapKey
#    - Wait ~10s for cb-controller's reporter tick (DIVERGENCE_REPORTER_INTERVAL=10s)
#    - GET orb's /api/v1/divergence — entry should appear
#    - POST orb's /api/v1/divergence/publish — pushes snapshot to MinIO
#    - Wait ~10s for orbital ingester (ORBITAL_DIVERGENCE_POLL_INTERVAL=10s default)
#    - GET orbital's /api/v1/divergence?orbId=colo:<serviceTag>-idrac → orbital has the entry
#    - POST orbital's /api/v1/divergence/<id>/accept {} — dispatches updateServer mutation
#    - Verify with a DGraph query that the field actually changed
```
