---
description: >-
  Prerequisites, the commands to bring up Orbital and Orb locally with example data, and how to fix the common failures.
sidebar_label: Run it locally
sidebar_position: 1
---

# Run it locally

This brings up Orbital, Orb, and everything they depend on, with example data loaded. It takes a few minutes.

## Prerequisites

- **Go 1.26** or later
- **Docker** and Docker Compose
- **Make**

Node.js is only needed if you are editing stylesheets.

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
make up            # dependencies: DGraph, PostgreSQL, MinIO, a registry
```

```bash
make run-orbital   # the cloud service, on :8001
```

```bash
make run-orb       # the edge service, on :8010
```

## Load example data

Once Orbital is up, in a fourth terminal:

```bash
make seed
```

This populates both graphs with example data centers, racks, servers and clusters.

## Open both UIs

| URL | What it is |
|---|---|
| http://localhost:8001 | Orbital — the cloud service |
| http://localhost:8010 | Orb — one site's edge service |
| http://localhost:8001/graphql | GraphQL playground |
| http://localhost:8001/swagger/index.html | REST API reference |

Open the two UIs side by side. They render the same pages from the same templates, which makes the difference between authoring intent and mirroring it easy to see.

## Stopping

```bash
make down
```

## If something goes wrong

| Symptom | Cause |
|---|---|
| `dgraph: executable file not found` | `make dev-deps` did not finish, or `~/.local/bin` is not on `PATH` |
| Port already in use | Another process holds `:8001`, `:8010`, `:8080` or `:5432`. Free it and retry. |
| Seed fails with a schema mismatch | `make down && make up && make seed` |
| Orb shows no imported data | Run an export in Orbital first, then refresh |
