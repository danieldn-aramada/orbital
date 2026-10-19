---
description: >-
  Prerequisites, the commands to bring up Orbital and Orb locally with example data, and how to fix the common failures.
sidebar_label: Run it locally
sidebar_position: 1
---

# Run it locally

This brings up Orbital, Orb, and everything they depend on, with an example data center loaded. It takes a few minutes.

## Prerequisites

- **Go 1.26** or later
- **Docker** and Docker Compose
- **Make**
- **`jq`** and **`curl`**, which the seed script uses

Node.js is only needed if you are editing stylesheets or the docs site.

## Get the source

```bash
git clone https://github.com/danieldn-aramada/orbital.git
cd orbital
```

Every command below runs from the repository root.

## One-time setup

```bash
make dev-deps
```

This installs a DGraph wrapper into `~/.local/bin`. That directory must be on your `PATH`.

```bash
which dgraph
```

If that prints nothing, add `~/.local/bin` to your `PATH` and try again.

## Start the stack

Each of these runs in its own terminal and stays running.

```bash
make up            # dependencies, in Docker
make run-orbital   # the cloud service, on :8001
make run-orb       # the edge service, on :8010
```

`make up` starts:

| Service | Port | Used by |
|---|---|---|
| DGraph | `:8080` | Orbital's graph |
| DGraph (scratch) | `:8081` | Orbital, to build each export in isolation |
| DGraph (Orb) | `:8082` | Orb's graph |
| PostgreSQL | `:5432` | Orbital's users, audit log and jobs |
| MinIO | `:9000` | Object storage, for backups and divergence reports |
| OCI registry | `:5001` | Published exports |
| Ratel | `:8000` | A DGraph admin UI, for poking at the raw graph |

## Load example data

Once Orbital is up, in a fourth terminal:

```bash
make seed
```

This loads the **`docs-example`** data center into both graphs — a rack, two servers, a switch they are cabled to, and a Kubernetes cluster running on them. Every example and screenshot on this site uses it.

It also creates a local administrator, `admin@example.com` with password `admin`.

## Open both UIs

| URL | What it is |
|---|---|
| http://localhost:8001 | Orbital — the cloud service. Sign in as `admin@example.com` / `admin`. |
| http://localhost:8010 | Orb — one data center's edge service |
| http://localhost:8001/graphql | GraphQL explorer, once signed in |
| http://localhost:8001/swagger/index.html | REST API reference |

Open the two UIs side by side. They render the same pages from the same templates, which makes the difference between authoring intent and mirroring it easy to see.

## Stopping

```bash
make down
```

:::warning
`make down` removes the containers' volumes, so the graph and the database start empty next time. Run `make seed` again after `make up`.
:::

## If something goes wrong

| Symptom | Cause |
|---|---|
| `dgraph: executable file not found` | `make dev-deps` did not finish, or `~/.local/bin` is not on `PATH` |
| Port already in use | Another process holds `:8001`, `:8010`, `:8080` or `:5432`. Free it and retry. |
| Seed fails with a schema error | The graph holds an older schema. `make down && make up && make seed` |
| Sign-in fails | `make seed` creates the user. Run it after Orbital has started, so the database tables exist. |
