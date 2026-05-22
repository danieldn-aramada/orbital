# Contributing

## Setup

```bash
# Terminal 1 — start all dependencies (orbital + orb DGraph, PostgreSQL, registry) then start orbital
make up
make run-orbital

# Terminal 2 — start orb
make run-orb

# Terminal 3 - Seed example data (once, after orbital is up)
make seed
```

Both UIs should open without errors:
- Orbital: http://localhost:8001
- Orb: http://localhost:8010

No `.env` sourcing required — all local defaults are baked into `config.go` and `orbconfig/config.go`.

## Running tests

```bash
make test-unit          # no services required
make test-integration   # requires: make up
make test-e2e           # requires: make run-orbital running in another terminal
make test-e2e-orb       # requires: make run-orb running in another terminal
```

`test-integration` re-seeds DGraph after running so `test-e2e` always starts from a known state.

To open the interactive Playwright UI for orbital e2e tests:
```bash
npx playwright test --ui
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

This project uses [Claude Code](https://claude.ai/code) for AI-assisted development. If you haven't used it before:

**Install:**
```bash
npm install -g @anthropic-ai/claude-code
```

See [claude.ai/code](https://claude.ai/code) for full setup and documentation.

**Start a session:**
Run `claude` in the repo root. Claude automatically reads `CLAUDE.md` at session start — it already knows the architecture, conventions, and settled decisions. You don't need to re-explain the project each session.

**Two files to know:**
- **`CLAUDE.md`** — conventions, architecture decisions, and settled rules that Claude follows. Update it when any of these change.
- **`AI.md`** — minimal audit log. Append a row to the table when AI assistance was used in a PR.

**Tip:** If Claude suggests something that conflicts with an established decision, point it to the Settled Decisions section in `CLAUDE.md`.

## PR checklist

See [`.github/pull_request_template.md`](.github/pull_request_template.md) — GitHub populates this automatically when opening a PR.
