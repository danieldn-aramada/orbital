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

## PR checklist

See [`.github/pull_request_template.md`](.github/pull_request_template.md) — GitHub populates this automatically when opening a PR.
