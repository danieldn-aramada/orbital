# Contributing

## Setup

```bash
make up           # Terminal 1 — start all dependencies
make run-orbital  # Terminal 2 — orbital on :8001
make run-orb      # Terminal 3 — orb on :8010
make seed         # once, after orbital is up
```

Both UIs should open without errors:
- Orbital: http://localhost:8001
- Orb: http://localhost:8010

No `.env` sourcing required — all local defaults are baked into `config.go` and `orbconfig/config.go`.

## Running tests

```bash
make test  # run all tests (requires: make up + make run-orbital + make run-orb)
```

## Editing styles (CSS)

Orbital uses [Bulma](https://bulma.io/) compiled from SASS. **Do not edit `web/static/css/main.css` directly** — it is generated and will be overwritten.

Edit `web/sass/main.scss` instead, then rebuild:

```bash
make build-css       # one-time compile
make watch-css       # watch mode — recompiles on every save
```

Requires `npm install` once to install the `sass` compiler.

## Development workflow

- Branch from `main`, PR back to `main`
- No force pushes to `main`

## Using Claude Code

Run `claude` in the repo root. `CLAUDE.md` is loaded automatically — architecture, conventions, and settled decisions are already in context.

- **`CLAUDE.md`** — the source of truth for AI behavior in this repo. Update it when architectural decisions or conventions change.
- **`AI.md`** — append a row when AI assistance was used in a PR.

See [claude.ai/code](https://claude.ai/code) for install and setup.

## Releasing the orbital CLI

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
- Clones `danieldn-aramada/homebrew-tools`, updates `Formula/orbital.rb` with the new URLs and SHA256s, and pushes

**2. Verify the release**

```bash
gh release view cli/v0.0.2 --repo danieldn-aramada/orbital
```

**3. Smoke-test via Homebrew**

```bash
brew update
brew upgrade orbital    # or: brew install orbital (first time)
orbital version         # should print v0.0.2
orbital completion zsh  # should print a zsh completion script
```

### What the formula does

On `brew install orbital`, Homebrew:
1. Downloads the architecture-appropriate tarball
2. Installs the `orbital` binary to `$(brew --prefix)/bin/`
3. Runs `orbital completion bash/zsh/fish` and installs each script to the correct system completions directory

Shell completions are active automatically for zsh (default macOS shell) as long as `$(brew --prefix)/share/zsh/site-functions` is in `$fpath` — Homebrew adds this by default.

### Versioning rules

| Binary | Tag format | Example |
|--------|-----------|---------|
| orbital server | `v*` | `v0.1.0` |
| orbital CLI | `cli/v*` | `cli/v0.0.2` |
| orb | `orb/v*` | `orb/v0.0.1` |

Never use a bare `v*` tag for the CLI — it will be picked up by `git describe` as a server version.

## PR checklist

See [`.github/pull_request_template.md`](.github/pull_request_template.md) — GitHub populates this automatically when opening a PR.
