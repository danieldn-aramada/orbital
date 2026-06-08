# AKS Dev Overlays

Two overlays target different AKS namespaces. Both share the same base
(`deploy/base/`) and follow the same apply pattern.

## Overlays

| Overlay | Namespace | PostgreSQL | Notes |
|---|---|---|---|
| `dev-netbox` | `netbox` | Azure managed (external) | Original deployment |
| `dev-orbital` | `orbital` | In-cluster StatefulSet | Newer, isolated stack |

## Deploying

### 1. Create the secrets file (one-time per machine)

`secrets.yaml` is gitignored and must be created locally before applying.
Copy the template and fill in the real values:

```bash
cp deploy/dev/secrets.yaml deploy/overlays/dev-netbox/secrets.yaml
# or
cp deploy/dev/secrets.yaml deploy/overlays/dev-orbital/secrets.yaml
```

**For `dev-netbox`** — update `DATABASE_URL` to the Azure managed PostgreSQL
connection string. All other values in the template are correct for the
netbox namespace.

**For `dev-orbital`** — update `ORBITAL_OIDC_REDIRECT_URL` to the AKS ingress
URL for the orbital namespace (e.g.
`https://orbital.devnew.armada.internal/auth/callback`). The `DATABASE_URL`
in the template points to the in-cluster `orbital-postgres` service and does
not need changing.

### 2. Update the image tag

Edit the overlay's `kustomization.yaml` and bump `newTag` to the version you
are deploying:

```yaml
images:
  - name: armadaeksatest.azurecr.io/orbital
    newTag: v0.0.15   # ← set this
```

### 3. Build and push the image

```bash
git tag v0.0.15
git push origin main v0.0.15
make push   # builds and pushes armadaeksatest.azurecr.io/orbital:v0.0.15
```

If you amend the commit after tagging, move the tag before pushing:

```bash
git tag -f v0.0.15
git push origin main v0.0.15 --force
make push
```

### 4. Apply

```bash
kubectl apply -k deploy/overlays/dev-netbox
# or
kubectl apply -k deploy/overlays/dev-orbital
```

Dry-run first if you want to verify without applying:

```bash
kubectl apply -k deploy/overlays/dev-netbox --dry-run=client
```

## Seeding an AKS dev environment

Run once after initial deployment, or any time you need to reset graph data.
Seeding requires port-forwarding to AKS services — open terminals for each forward
and leave them running while the seed scripts execute.

### Step 1 — Port-forward DGraph

Open three terminals (or background the commands with `&`):

```bash
# Terminal 1 — DGraph blue (production graph)
kubectl port-forward svc/dgraph-blue-dgraph-alpha 8080:8080 -n <namespace>

# Terminal 2 — DGraph scratch alpha
kubectl port-forward svc/dgraph-scratch-dgraph-alpha 8081:8080 -n <namespace>

# Terminal 3 — DGraph scratch zero
kubectl port-forward svc/dgraph-scratch-dgraph-zero 6081:6080 -n <namespace>
```

Replace `<namespace>` with `netbox` or `orbital`.

### Step 2 — Seed DGraph

```bash
# Seed schema + example data (additive, safe to re-run)
./scripts/seed-dgraph.sh

# Or start clean (drops all existing graph data first)
./scripts/seed-dgraph.sh --clean
```

`seed-dgraph.sh` connects to `localhost:8080` (blue) and `localhost:8081` (scratch)
— the port-forwards from Step 1 must be active.

### Step 3 — Seed PostgreSQL (users)

Creates `admin@armada.ai` / `admin` and `user@armada.ai` / `user`.

**`dev-orbital`** (in-cluster PostgreSQL):

```bash
# Terminal 4 — PostgreSQL
kubectl port-forward svc/orbital-postgres 5432:5432 -n orbital

# Then in another terminal:
./scripts/seed-aks-postgres.sh --namespace orbital
```

**`dev-netbox`** (Azure managed PostgreSQL — no port-forward needed):

```bash
# Use the DATABASE_URL from your secrets.yaml directly:
psql "<DATABASE_URL>" -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, role, created_at)
  VALUES ('admin@armada.ai', 'Admin', 'admin@armada.ai',
    '\$2a\$12\$Wb3DtBrZbW9528J/FKL81ON73s7PEPNkup9FN8JN.jGBtM03.sckG', true, 'admin', NOW())
  ON CONFLICT (email) DO UPDATE SET role = 'admin';
"
```

> The bcrypt hash is for password `admin` (cost 12). Local/dev only.

### Shortcut: let the scripts manage port-forwards

The `seed-aks.sh` script opens and closes all DGraph port-forwards automatically:

```bash
./scripts/seed-aks.sh --namespace orbital          # seed only
./scripts/seed-aks.sh --namespace orbital --clean  # drop + seed
```

Similarly, `seed-aks-postgres.sh` manages the PostgreSQL port-forward for `dev-orbital`:

```bash
./scripts/seed-aks-postgres.sh --namespace orbital
```

## What's in the base

`deploy/base/` contains resources applied to **every** overlay:

- `deploy.yaml` — orbital Deployment and Service
- `scratch-exports-pvc.yaml` — PVC for subgraph export scratch space
- `dgraph-exports-pvc.yaml` — PVC for DGraph export output
- `virtualservice.yaml` — Istio VirtualService for ingress routing

`dev-orbital` additionally includes:

- `postgres.yaml` — in-cluster PostgreSQL StatefulSet

`dev-netbox` uses Azure managed PostgreSQL and has no in-cluster database.
