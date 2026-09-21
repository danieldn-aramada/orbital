# Manual Deployment

## Local

Use `make up` + `make run-orbital` for day-to-day local dev. The instructions
below cover building a container image and running it standalone, which is
rarely needed.

Start dependencies (DGraph + PostgreSQL):
```bash
make up
```

Build and run orbital from a container:
```bash
docker build -t orbital:dev .

docker run -p 8001:8001 \
  -e DGRAPH_URL=http://host.docker.internal:8080/graphql \
  orbital:dev
```

---

## AKS Dev

Canonical AKS deployment uses **kustomize overlays** in `deploy/overlays/`.
Two overlays target different namespaces; both share `deploy/base/`.

| Overlay | Namespace | PostgreSQL | Notes |
|---|---|---|---|
| `dev-netbox` | `netbox` | Azure managed (external) | Original deployment |
| `dev-orbital` | `orbital` | In-cluster StatefulSet | Newer, isolated stack |

> Raw manifests in `deploy/legacy/` exist for reference only — not the
> recommended path. Use the overlays.

### Prerequisites

- `kubectl` context pointing at the dev AKS cluster
- `helm` installed
- Push access to `armadaeksatest` ACR — see step 4
- Istio installed in the cluster (orbital ingress is an Istio VirtualService)
- Azure managed PostgreSQL connection string (for `dev-netbox` only)

### 1. Determine the orbital hostname

Orbital must be publicly reachable for OIDC to work. Get the Istio ingress
gateway's external IP:

```bash
kubectl get svc -n istio-system istio-ingressgateway
# Note the EXTERNAL-IP
```

Choose a hostname (e.g. `orbital-dev.<external-ip>.nip.io` for a quick
no-DNS option, or a real DNS name). The VirtualService in `deploy/base/`
references this hostname.

### 2. Register the redirect URI in Keycloak

Orbital authenticates against Keycloak, not Entra directly (Keycloak brokers to
Entra upstream). In the `armada` realm, client **armada-orbital**:
- **Settings → Valid redirect URIs** → add `https://<hostname>/auth/callback`
- The URI is compared byte-for-byte, so it must match `ORBITAL_OIDC_REDIRECT_URL`
  exactly — scheme, host, port and path.

Keycloak accepts `http://` and private hostnames, unlike Entra. That is why
orbital could drop the device-code flow: it existed only to work around Entra
refusing a redirect URI it could not resolve.

### 3. Create the secrets file

`secrets.yaml` is gitignored per overlay and must be created locally before
applying. Copy the template and fill in the real values:

```bash
cp deploy/legacy/secrets.yaml deploy/overlays/dev-netbox/secrets.yaml
# or
cp deploy/legacy/secrets.yaml deploy/overlays/dev-orbital/secrets.yaml
```

> The template lives under `deploy/legacy/` because the original raw-manifest
> workflow used it directly. Overlays now read `secrets.yaml` from their own
> directory — each is gitignored. A dedicated `secrets.example.yaml` per
> overlay would be cleaner; tracked as a followup.

Generate session keys if you need fresh ones:
```bash
# HMAC key — any random string
openssl rand -hex 32

# Encryption key — MUST be exactly 32 bytes
LC_ALL=C tr -dc 'a-zA-Z0-9!@#$%^&*' < /dev/urandom | head -c32
```

**For `dev-netbox`** — update `DATABASE_URL` to the Azure managed PostgreSQL
connection string. The cosign signing key (`cosign.key`) lives in the secret
alongside the other values — generate once with `cosign generate-key-pair`
if needed.

**For `dev-orbital`** — update `ORBITAL_OIDC_REDIRECT_URL` to the AKS ingress
URL for the orbital namespace. The `DATABASE_URL` in the template points to
the in-cluster `orbital-postgres` service.

### 4. Build and push the image

Requires push access to
[armadaeksatest](https://portal.azure.com/#@armada.ai/resource/subscriptions/212ddfb2-b7cf-4041-8eed-8882792f8d41/resourceGroups/eksa-acr-test/providers/Microsoft.ContainerRegistry/registries/armadaeksatest/repository).

The image tag is derived from `git describe` against the latest `v*` tag.
Tag the release first, then push:

```bash
git tag v0.0.17                    # bump as appropriate
git push origin main v0.0.17
az acr login --name armadaeksatest
make push                          # pushes armadaeksatest.azurecr.io/orbital:v0.0.17
```

`make push` reads `SERVER_VERSION` from `git describe` — if the working tree
is dirty the tag will include `-dirty`. Commit/stash first.

If you amend the commit after tagging, move the tag before pushing:
```bash
git tag -f v0.0.17
git push origin main v0.0.17 --force
make push
```

### 5. Set the image tag in the overlay

Edit the overlay's `kustomization.yaml` and bump `newTag` to match what you
just pushed:

```yaml
images:
  - name: armadaeksatest.azurecr.io/orbital
    newTag: v0.0.17   # ← set this
```

### 6. Deploy DGraph (blue + scratch)

Blue is the live instance serving the Topology API. Scratch is used
exclusively for subgraph exports (blue-green pattern).

```bash
# Pick the target namespace based on overlay choice
NS=netbox   # or "orbital"

helm upgrade --install dgraph-blue ./deploy/charts/dgraph \
  --namespace "$NS" \
  --values deploy/charts/values-dev.yaml

helm upgrade --install dgraph-scratch ./deploy/charts/dgraph \
  --namespace "$NS" \
  --values deploy/charts/values-dev-scratch.yaml

kubectl rollout status statefulset/dgraph-blue-dgraph-alpha -n "$NS"
kubectl rollout status statefulset/dgraph-scratch-dgraph-alpha -n "$NS"
```

### 7. Apply the overlay

```bash
kubectl apply -k deploy/overlays/dev-netbox
# or
kubectl apply -k deploy/overlays/dev-orbital
```

Dry-run first to verify without applying:
```bash
kubectl apply -k deploy/overlays/dev-netbox --dry-run=client
```

Watch the rollout:
```bash
kubectl rollout status deployment/orbital -n "$NS"
kubectl logs -f deployment/orbital -n "$NS"
```

> **Orbital does NOT apply the DGraph schema — not on first boot, not on rollout.** The rollout
> alone leaves DGraph on the old schema. See step 8.

### 8. Apply the DGraph schema — REQUIRED after any `schema/VERSION` bump

**Nothing applies it for you.** Not orbital on boot, not the rollout, not `kubectl apply -k`.
An image whose queries reference a new field, running against a DGraph still on the old schema,
returns errors → zero rows → a 404 or a silently truncated page. This has happened: v0.0.25 added
`retentionDays` while AKS DGraph was on v3, and **every cluster 404'd, then the edit modal
vanished** (2026-07-27).

Apply it **before or with** the rollout, not after users hit 404s:

```bash
kubectl port-forward -n "$NS" svc/dgraph-blue-dgraph-alpha 8080:8080 &
curl -X POST localhost:8080/admin/schema \
  -H 'Content-Type: application/graphql' --data-binary @schema/schema.graphql
```

Additive changes (new nullable fields, new enum values) are non-destructive. Some are not:
`v7` added `@search` to `ConfigItem.version`, which reindexes every ConfigItem and **blocks
mutations until it returns** — schedule those like a migration. Per-version notes are in
[DGRAPH.md](../docs/reference/DGRAPH.md) § Schema rules.

`main.go` does migrate at boot, but that is the **PostgreSQL** ent schema — a different system with
a confusingly similar name. It tells you nothing about DGraph's.

Step 10 also applies the schema (it is the first thing `seed-aks.sh` does), so if you are running
that anyway, it covers this step.

### 8b. Break-glass access — know this before you need it

Orbital's local password login always works, and is **not** gated on SSO. If your
identity provider breaks — a bad group mapping, a dropped claim, an expired
client secret — an admin with local credentials can still sign in and repair it.

This is the recovery path. There is deliberately no special-casing inside the
authorization logic to rescue you, because every comparable product answers this
the same way: ArgoCD keeps a local `admin` account as documented break-glass,
Grafana keeps local admin login alongside SSO, Kubernetes keeps x509 client certs
and the admin kubeconfig, Vault has the root token.

What that means operationally:

- Keep at least one local admin account with a known password, and keep that
  password somewhere your team can reach during an incident.
- `ORBITAL_ADMIN_EMAILS` is **not** this. It promotes named emails to admin at
  first login — that is bootstrapping, how the first admin gets in. Break-glass is
  getting back in afterwards.
- A provider misconfiguration in `roleMapping` mode denies everyone whose groups
  stop matching, including admins, at their next login. The local account is how
  you fix the config.

### 9. Seed PostgreSQL admin user

Creates `admin@armada.ai` / `admin` and `user@armada.ai` / `user`.

**For `dev-netbox`** (Azure managed PostgreSQL — no port-forward needed):
```bash
psql "<DATABASE_URL>" -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, role, created_at)
  VALUES ('admin@armada.ai', 'Admin', 'admin@armada.ai',
    '\$2a\$12\$Wb3DtBrZbW9528J/FKL81ON73s7PEPNkup9FN8JN.jGBtM03.sckG', true, 'admin', NOW())
  ON CONFLICT (email) DO UPDATE SET role = 'admin';
"
```

**For `dev-orbital`** (in-cluster PostgreSQL):
```bash
./scripts/seed-aks-postgres.sh --namespace orbital
```

> The bcrypt hash is for password `admin` (cost 12). Dev only.

### 10. Seed DGraph (optional — example data)

Run once after initial deployment, or any time you need to reset graph data.

**"Optional" refers to the example data, not the schema.** `seed-aks.sh` applies
`schema/schema.graphql` to blue and scratch before it seeds anything, so running this covers
step 8 — but it applies the schema **from your local working tree**, which may differ from the one
baked into the deployed image. Step 8 is the path to use when you only want the schema.
The `seed-aks.sh` script opens and closes DGraph port-forwards automatically:

```bash
./scripts/seed-aks.sh --namespace "$NS"          # additive seed (safe to re-run)
./scripts/seed-aks.sh --namespace "$NS" --clean  # drop all existing graph data first
```

Manual port-forward (if the script doesn't fit your workflow):
```bash
# Terminal 1 — blue (production graph)
kubectl port-forward svc/dgraph-blue-dgraph-alpha 8080:8080 -n "$NS"

# Terminal 2 — scratch alpha
kubectl port-forward svc/dgraph-scratch-dgraph-alpha 8081:8080 -n "$NS"

# Terminal 3 — scratch zero
kubectl port-forward svc/dgraph-scratch-dgraph-zero 6081:6080 -n "$NS"

# Then in another terminal:
./scripts/seed-dgraph.sh         # or --clean
```

### Verify

```bash
# Pod is ready (readiness probe hits /healthz — 10s initial delay)
kubectl get pods -n "$NS" -l app=orbital

# VirtualService configured
kubectl get virtualservice -n "$NS"

# Smoke tests
kubectl port-forward svc/orbital 8001:8001 -n "$NS" &
make smoke-aks
```

The readiness probe is intentionally a no-op `/healthz` that always
returns 200 — it confirms the process is bound to its port. There is no
liveness probe; transient DB failures should not cause restart loops.

### Troubleshooting

Exec into the orbital pod (image includes `curl`, `bash`, `bind-tools`,
`netcat-openbsd`, `procps`):

```bash
kubectl exec -it deployment/orbital -n "$NS" -- bash
```

From inside the pod:
```bash
curl -s http://dgraph-blue-dgraph-alpha:8080/health    # blue DGraph reachable
nslookup orbital-postgres                              # if using in-cluster PG
ps auxf                                                # running processes
```

### What's in the base

`deploy/base/` contains resources applied to **every** overlay:

- `deploy.yaml` — orbital Deployment and Service
- `scratch-exports-pvc.yaml` — PVC for subgraph export scratch space
- `dgraph-exports-pvc.yaml` — PVC for DGraph export output
- `virtualservice.yaml` — Istio VirtualService for ingress routing

`dev-orbital` additionally includes `postgres.yaml` for the in-cluster
PostgreSQL StatefulSet.

## High availability

**Orbital is not in the control path.** Nothing in the cloud executes against a
modular data center, authoritative reconcilers run locally, and the CMDB is not
in the reconciliation path. Orb serves intent at the edge, offline. An orbital
outage means operators cannot change intent and orbs cannot pull new intent —
it does not degrade a data center. Size the availability target accordingly.

**The service is one Deployment. Scale it.** Every replica is identical and any
replica count is safe. Background work is coordinated through PostgreSQL —
advisory locks for "may I act now", leases for "is that runner still alive" —
not through replica counts or operator runbooks. There is no worker tier, no
leader to configure, and no pod-to-pod networking to open.

| Concern | How it is safe at N replicas |
|---|---|
| Export / backup / restore execution | Claimed with a conditional `UPDATE`; exactly one runner wins. A runner proves liveness with `heartbeat_at` and is fenced on `locked_by`, so one that stalls and resumes aborts instead of racing its replacement. |
| Job admission | The check-then-create window is held under a transaction-scoped advisory lock, so two triggers cannot both find "nothing running". |
| Dead runners | A reaper sweeps on a ticker and fails jobs whose heartbeat went stale. It does **not** run at boot — a starting pod must never assume another pod's running job is dead. |
| Schema migration at startup | Every replica migrates at boot, under an advisory lock so they cannot race the same DDL. Without it a **fresh install at two replicas crashloops one pod deterministically** — an empty database means both try to create every table. |
| Backup schedule | `fire()` takes an advisory lock, so one backup per cron tick regardless of replica count. |
| Divergence ingest | A report is claimed by advancing the cursor *before* applying it, so exactly one replica ingests. |
| Sessions, CSRF, OIDC state | Cookie-based off `ORBITAL_SESSION_HMAC_KEY`. No session store, no sticky sessions. |
| Caching | Every cache is per-request by design. Valkey is not required. |

### Requirements

- **Highly available PostgreSQL.** It holds all coordination state.
- **Highly available DGraph.** Orbital in front of a single alpha is not highly
  available however many orbital pods run. See the DGraph chart values —
  `zero.replicaCount` and `alpha.replicaCount` are `1` in `values-dev.yaml`.
- **`ReadWriteMany` export volumes**, so any replica can serve a download any
  other replica produced. Both PVCs already use `azurefile-csi`.
- All replicas must share `ORBITAL_SESSION_HMAC_KEY` (one Secret — already the case).

### Known limits

- **Rate limiting is per-pod and therefore approximate.** The effective ceiling
  is `ORBITAL_RATE_LIMIT_RPS x replicas`, including the tighter login bucket.
  Divide the configured value by the expected replica count. Making it exact
  would promote Valkey from optimisation to hard dependency.
- **The connection pool is per-pod too.** Total load on PostgreSQL is
  `DefaultMaxConns (10) x replicas`; keep it under `max_connections`.
- **`strategy: Recreate` with no PodDisruptionBudget** means orbital is still
  down during every deploy and every node drain — at any replica count. Fixing
  that is a separate change from replica safety.
