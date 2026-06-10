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

### 2. Update Azure AD redirect URI

In the Azure AD app registration for orbital (client ID
`5fc832f6-843e-4207-93dd-b3c3a77c06f2`):
- Go to **Authentication → Redirect URIs**
- Add: `https://<hostname>/auth/callback`

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

Orbital applies the DGraph schema on first boot.

### 8. Seed PostgreSQL admin user

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

### 9. Seed DGraph (optional — example data)

Run once after initial deployment, or any time you need to reset graph data.
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
