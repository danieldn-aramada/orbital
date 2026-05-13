# Manual Deployment

## Local

Start dependencies (DGraph + PostgreSQL):
```bash
docker-compose -f deploy/local/docker-compose.yml up -d
```

Build and run orbital:
```bash
docker build -t orbital:v0.0.1 .

docker run -p 8001:8001 \
  -e DGRAPH_URL=http://host.docker.internal:8080/graphql \
  orbital:v0.0.1
```

---

## AKS Dev

### Prerequisites

- `kubectl` context pointing at the dev AKS cluster
- `helm` installed
- Access to `armadaeksatest` ACR (see link in build step)
- nginx ingress controller installed in the cluster
- Azure managed PostgreSQL instance (connection string ready)

### 1. Determine the orbital hostname

Orbital must be publicly reachable for OIDC to work. Get the ingress controller's external IP:

```bash
kubectl get svc -n ingress-nginx ingress-nginx-controller
# Note the EXTERNAL-IP
```

Choose a hostname (e.g. `orbital-dev.<external-ip>.nip.io` for a quick no-DNS option, or a real DNS name).
Update `deploy/dev/ingress.yaml` — replace `orbital-dev.REPLACE_ME` with the actual hostname.

### 2. Update Azure AD redirect URI

In the Azure AD app registration for orbital (client ID `5fc832f6-843e-4207-93dd-b3c3a77c06f2`):
- Go to **Authentication → Redirect URIs**
- Add: `https://<hostname>/auth/callback`

### 3. Create Kubernetes secrets

All secrets go into a namespace of your choice (e.g. `orbital-dev`). Create the namespace first if needed:

```bash
kubectl create namespace orbital-dev
```

Generate session keys:
```bash
# HMAC key — any random string
openssl rand -hex 32

# Encryption key — MUST be exactly 32 bytes
LC_ALL=C tr -dc 'a-zA-Z0-9!@#$%^&*' < /dev/urandom | head -c32
```

Create the main secrets:
```bash
kubectl create secret generic orbital-secrets \
  --namespace orbital-dev \
  --from-literal=DATABASE_URL='postgres://orbital:<password>@<host>:5432/orbital?sslmode=require' \
  --from-literal=ORBITAL_SESSION_HMAC_KEY='<64-char hex from openssl above>' \
  --from-literal=ORBITAL_SESSION_ENCRYPTION_KEY='<exactly-32-chars>' \
  --from-literal=ORBITAL_OIDC_CLIENT_SECRET='<client secret from Azure AD>' \
  --from-literal=ORBITAL_OIDC_REDIRECT_URL='https://<hostname>/auth/callback' \
  --from-literal=ORBITAL_S3_SECRET_KEY='<azure blob storage access key>' \
  --from-literal=ORBITAL_OCI_PASSWORD='<ACR admin password>'
```

Create the cosign key secret (generate once with `cosign generate-key-pair` if you don't have one):
```bash
kubectl create secret generic orbital-cosign-key \
  --namespace orbital-dev \
  --from-file=cosign.key=./cosign.key
```

### 4. Build and push orbital

Requires access to the Sandbox Services Landing Zone and push access to
[armadaeksatest](https://portal.azure.com/#@armada.ai/resource/subscriptions/212ddfb2-b7cf-4041-8eed-8882792f8d41/resourceGroups/eksa-acr-test/providers/Microsoft.ContainerRegistry/registries/armadaeksatest/repository).

```bash
az login
az acr login --name armadaeksatest

# Bump the version tag in deploy/dev/deploy.yaml first, then:
docker buildx build \
  --platform linux/amd64 \
  -t armadaeksatest.azurecr.io/orbital:v0.1.0 \
  --push .
```

### 5. Deploy DGraph (two instances)

Blue is the live instance. Scratch is used exclusively for subgraph exports.

```bash
# Blue — live, serves Topology API
helm upgrade --install dgraph-blue ./deploy/charts/dgraph \
  --namespace orbital-dev \
  --values deploy/charts/values-dev.yaml

# Scratch — export only, no Ratel
helm upgrade --install dgraph-scratch ./deploy/charts/dgraph \
  --namespace orbital-dev \
  --values deploy/charts/values-dev-scratch.yaml
```

Wait for both to be ready:
```bash
kubectl rollout status statefulset/dgraph-blue-dgraph-alpha -n orbital-dev
kubectl rollout status statefulset/dgraph-scratch-dgraph-alpha -n orbital-dev
```

### 6. Apply the DGraph schema

Once orbital is running (step 7), it applies the schema automatically on startup. If you need to apply manually:
```bash
kubectl run schema-apply --rm -it --restart=Never \
  --namespace orbital-dev \
  --image=curlimages/curl -- \
  curl -sf -X POST http://dgraph-blue-dgraph-alpha:8080/admin/schema \
  -H "Content-Type: application/graphql" \
  --data-binary @- < schema/schema-demo.graphql
```

### 7. Deploy orbital

```bash
kubectl apply -f deploy/dev/deploy.yaml -n orbital-dev
kubectl apply -f deploy/dev/ingress.yaml -n orbital-dev
```

Watch startup logs:
```bash
kubectl logs -f deployment/orbital -n orbital-dev
```

Orbital applies PostgreSQL migrations and DGraph schema on first boot.

### 8. Create the admin user

Orbital's local login requires a seed user in PostgreSQL. Run against your Azure managed PostgreSQL:
```sql
INSERT INTO users (id, email, password_hash, name, created_at)
VALUES (gen_random_uuid(), 'admin@armada.ai', '<bcrypt hash>', 'Admin', now());
```

Or use `make seed-users` if that target exists, pointing at the prod DB URL.

### Verify

```bash
# Orbital pod running
kubectl get pods -n orbital-dev -l app=orbital

# Ingress has an address
kubectl get ingress orbital -n orbital-dev

# Orbital health (from inside cluster)
kubectl run curl-test --rm -it --restart=Never --namespace orbital-dev \
  --image=curlimages/curl -- curl -s http://orbital/health
```
