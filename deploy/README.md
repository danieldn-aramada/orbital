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

### Deploy DGraph

```bash
helm install dgraph ./deploy/charts/dgraph \
  --namespace <namespace> \
  --values deploy/charts/values-dev.yaml

# or upgrade if already installed
helm upgrade dgraph ./deploy/charts/dgraph \
  --namespace <namespace> \
  --values deploy/charts/values-dev.yaml
```

### Build and push orbital

Requires access to the Sandbox Services Landing Zone and push access to
[armadaeksatest](https://portal.azure.com/#@armada.ai/resource/subscriptions/212ddfb2-b7cf-4041-8eed-8882792f8d41/resourceGroups/eksa-acr-test/providers/Microsoft.ContainerRegistry/registries/armadaeksatest/repository).

```bash
az login
az acr login --name armadaeksatest

docker buildx build \
  --platform linux/amd64 \
  -t armadaeksatest.azurecr.io/orbital:v0.0.1 \
  --push .
```

### Deploy orbital

```bash
kubectl apply -f deploy/dev/deploy.yaml
kubectl apply -f deploy/dev/dgraph-network-policy.yaml
```
