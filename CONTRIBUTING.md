# Contributing

> **New here?** [README.md's Quick Start](README.md#quick-start) covers first-time setup and login. This file is the contributor reference: PRs, tests, code style, releases, tooling.

## Make commands at a glance

Run `make help` for the full list. The most-used:

| Daily | Tests | As needed |
|---|---|---|
| `make up` / `make down` | `make test-unit` | `make docs` (regen Swagger for both apps) |
| `make run-orbital` / `make run-orb` | `make test-integration` (needs `make up`) | `make build-css` (compile SCSS once) |
| | | `make docs-site-serve` (preview the docs site) |
| `make seed` | `make test-e2e` (needs orbital + orb running) | `make build-orbctl` (compile the CLI) |
| | `make test-e2e-ui` (interactive Playwright UI mode) | `make edge-up` / `make edge-down` (test orb against AKS upstream; see below) |
| | `make release-check` (pre-release; see below) | |

`make run-orbital` / `make run-orb` use `go run` for fast iteration. The host wrapper at `~/.local/bin/dgraph` (installed by `make dev-deps`) makes the orb import flow and orbital restore flow work under `go run` — ensure `~/.local/bin` is on your `PATH` or those paths fail with `dgraph: executable file not found`. `make release-check` exercises the same paths against the actual container images.

### Divergence flow gotchas

The full divergence workflow is covered in [`docs/runbooks/divergence-e2e-local.md`](docs/runbooks/divergence-e2e-local.md) (step-by-step local run, with the service/port inventory) and [`docs/reference/DIVERGENCE.md`](docs/reference/DIVERGENCE.md) (semantics). Two recurring footguns when working on it locally:

- **Orb doesn't auto-import.** Its poller only detects new tags; trigger imports with `POST /api/v1/import {"tag":"vN"}`.
- **cb-controller's reporter defaults to 5 min.** For local dev, set `DIVERGENCE_REPORTER_INTERVAL=15s` when running it.
- **End-to-end smoke**: `make e2e-divergence` runs the full export → publish → orb import → SSA admin override → orb publish → orbital ingest flow and asserts the divergence is visible. Requires orbital + orb + cb-bundler + cb-controller all running and minikube up.

## Running tests

Most of the time you only need:

```bash
make test-unit         # ~10s, no external services
make test-integration  # ~30s, requires: make up
make test-e2e          # ~30s, requires both UIs running (Playwright for orbital + orb)
```

The integration suite runs against its **own** DGraph (`dgraph-alpha-test`, `:8083`, started by
`make up`). It never touches the blue cluster on `:8080`, so your working graph survives a test run
and there is no ordering to get right. `testutil.ResetDGraphE` refuses to wipe `:8080`/`:8082` even
if something overrides the URL.

If export or backup tests fail with `resolving export failed because task failed`, run
`bash scripts/check-export-mounts.sh` — that error means a stale Docker bind mount, not a code bug.

Before cutting a release, run the full release-check flow once:

```bash
make release-check       # ~22 min — builds orbital + orb images, runs as containers,
                         # seeds, then runs the publish → import → restore checklist
make release-check-down  # tear down the release-check containers when finished
```

This validates the actual deployable artifacts (production code paths) — not just the `go run` dev path. If `release-check` is green and the unit + integration suites are green, the release is good to ship.

`make smoke-aks` runs the same Playwright suite against an AKS-deployed orbital (after `kubectl port-forward svc/orbital 8001:8001 -n netbox`). Use this to verify a fresh deploy works.

## Testing orb against an AKS-deployed orbital

When you want to validate orb's import flow against ConfigBundles that a real
AKS-deployed orbital pushed to ACR (instead of your local orbital), use the
edge sim. It brings up the full edge half as containers — local zot mirroring
upstream ACR, orb's DGraph, and **orb itself**. orb runs as a container
because `dgraph live` is Linux-only and can't run via `go run` on macOS.

```bash
cp deploy/local/sync-credentials.example.json deploy/local/sync-credentials.json
# Edit sync-credentials.json — fill in the ACR password

make edge-up        # builds orb image; starts zot + orb DGraph + orb container
                    # orb UI: http://localhost:8010
make edge-down      # tear down when finished
```

When you change orb code, re-run `make edge-up` — it rebuilds the orb image
and recreates the container (Docker layer cache keeps this to ~10-30s).

**Mutually exclusive with `make up`** — both bind :5001, :8082, and :9082. Run
one or the other, not both. The edge sim deliberately omits orbital and its
DGraph because the whole point is to test against an upstream-published bundle.

## Editing styles (CSS)

Orbital uses [Bulma](https://bulma.io/) compiled from SASS. **Do not edit `web/shared/static/css/main.css` directly** — it is generated and will be overwritten.

Edit `web/sass/main.scss` instead, then rebuild:

```bash
make build-css       # compile once
```

Requires `npm install` once to install the `sass` compiler.

## Editing Swagger annotations

If you touch any `@Router`, `@Tags`, or `@Summary` annotation in `internal/handler/*.go` or `internal/orbserver/*.go`, regenerate the OpenAPI specs:

```bash
make docs       # regenerates both orbital and orb swagger
```

Never hand-edit files under `docs/swagger.*` or `docs/orb/swagger.*` — they are generated.

## Editing the documentation site

The public docs site is a [Docusaurus](https://docusaurus.io) site in `docs/site/`. Its pages live in `docs/site/content/`, deliberately **not** `docs/site/docs/` — see the config comment. It is not the same thing as the rest of `docs/` — that directory is the maintainer reference tree and does not publish. See `.local/docs-site-design.md` for which content ships and which does not.

```bash
make docs-site          # build into website/build
make docs-site-serve    # build + serve on :3210
make docs-site-publish  # build + upload to Azure Blob (requires: az login)
```

For writing, `cd docs/site && npm start` gives hot reload on :3000. Use `make docs-site-serve` when you need **search** — the index is built at build time, so `npm start` has none.

### How the build works

`docusaurus build` pre-renders every route to its own HTML file, then bundles the JS that hydrates it in the browser. The output is 100% static — there is no server, which is why publishing is just uploading a folder.

`url` and `baseUrl` are read from the environment at build time, so the same source publishes anywhere:

```bash
make docs-site DOCS_URL=https://example.com DOCS_BASE_URL=/docs/
```

Defaults live at the top of the Makefile (`DOCS_ACCOUNT`, `DOCS_URL`, `DOCS_BASE_URL`).

### Publishing

`make docs-site-publish` uploads to the `$web` container of the `orbitaldocs` storage account, which has Azure's **static website** feature enabled. It uploads in two passes, deliberately:

| Pass | Files | `Cache-Control` |
|---|---|---|
| 1 | `assets/*` | `public, max-age=31536000, immutable` |
| 2 | everything else | `no-cache` |

Asset filenames are content-hashed, so they can cache forever — a new build produces new names. **HTML must not cache**, or a deploy is invisible to anyone who has visited before. Getting this backwards is the most common static-hosting complaint.

### Writing standard

Numeric targets measured against Infrahub, Crossplane, Temporal, Backstage and others are in `.local/docs-site-design.md` §5b. The short version: 14–18 words per sentence, paragraphs of 1–3 sentences, a heading every ~125 words, 1–3 admonitions per page, and a table wherever several items share the same attributes.

### Gotchas

- **Delete a page and rebuild** — `make docs-site` removes `build/` first, because Docusaurus does not prune and a deleted page otherwise survives in the output.
- **`onBrokenLinks` is `throw`** — a broken internal link fails the build. Raw `<a href="/...">` tags are **not** checked and will break under a non-root `baseUrl`; use `<Link to="...">` instead.
- **MDX is stricter than Markdown** — a bare `<` or `{` in prose fails the build. Keep placeholders like `<namespace>` inside backticks.
- **Mermaid renders client-side** — the static HTML contains an empty comment where the diagram goes, and it appears after hydration. That is expected, not a fault.

## Development workflow

- Branch from `main`, PR back to `main`
- No force pushes to `main`
- Tests must pass: `make test-unit` and `make test-integration` minimum before opening a PR. Run `make test-e2e` if you touched UI or routes. Run `make release-check` only when cutting a release.

## Using Claude Code

Run `claude` in the repo root. `CLAUDE.md` is loaded automatically — architecture, conventions, and settled decisions are already in context.

- **`CLAUDE.md`** — source of truth for AI behavior in this repo. Update it when architectural decisions or conventions change.

Common commands the team uses heavily:

| Command | When |
|---|---|
| `/effort max` | Before a session that settles a new design decision (switches to Opus). |
| `/effort normal` | After the design lands (switches back to Sonnet for implementation). |
| `/wrap-up` | End-of-session housekeeping — updates CLAUDE.md, saves memories, appends to ROADMAP.md's recent accomplishments. Run before closing any significant session. |
| `/rename` | Rename the current session for later findability. Run it early. |
| `/compact` | Compress context when the window grows large. Run when response quality drifts. |

See [claude.ai/code](https://claude.ai/code) for install and setup.

## Releasing the orbctl CLI

The CLI is versioned independently from the server. Tags follow `cli/v*` (e.g. `cli/v0.0.2`) so they don't collide with the server's bare `v*` lineage.

### Prerequisites

- `gh` CLI authenticated: `gh auth status` must succeed
- Write access to both `danieldn-aramada/orbital` and `danieldn-aramada/homebrew-tools`
- Clean, committed, pushed git tree on `main`

Verify:

```bash
git status          # must be clean
git push            # push any unpushed commits first
gh auth status
```

### Steps

**1. Run the release script**

```bash
./scripts/release-cli.sh v0.0.2
```

This does everything in one shot:
- Builds `darwin/arm64` and `darwin/amd64` binaries with the version and prod server URL baked in
- Creates and pushes the `cli/v0.0.2` tag
- Creates a GitHub release on `danieldn-aramada/orbital` with both tarballs attached
- Clones `danieldn-aramada/homebrew-tools`, updates `Formula/orbctl.rb` with the new URLs and SHA256s, and pushes

**2. Verify the release**

```bash
gh release view cli/v0.0.2 --repo danieldn-aramada/orbital
```

**3. Smoke-test via Homebrew**

```bash
brew update
brew upgrade orbctl          # already installed — picks up new version
# brew install orbctl        # first-time install
# brew reinstall orbctl      # force reinstall (formula-only change, same version)

orbctl version               # should print the new version
orbctl completion zsh        # should print a zsh completion script

# If completions don't work immediately, open a new terminal (or run: exec zsh)
# to reload the shell's completion index.
```

### What the formula does

On `brew install orbctl`, Homebrew:
1. Downloads the architecture-appropriate tarball
2. Installs the `orbctl` binary to `$(brew --prefix)/bin/`
3. Runs `orbctl completion bash/zsh/fish` and installs each script to the correct system completions directory

Shell completions are active automatically for zsh (default macOS shell) as long as `$(brew --prefix)/share/zsh/site-functions` is in `$fpath` — Homebrew adds this by default.

### Versioning rules

| Binary | Tag format | Example |
|--------|-----------|---------|
| orbital server | `v*` | `v0.1.0` |
| orbctl (CLI) | `cli/v*` | `cli/v0.0.2` |
| orb | `orb/v*` | `orb/v0.0.1` |

Never use a bare `v*` tag for the CLI — it will be picked up by `git describe` as a server version.

## PR checklist

See [`.github/pull_request_template.md`](.github/pull_request_template.md) — GitHub populates this automatically when opening a PR.
