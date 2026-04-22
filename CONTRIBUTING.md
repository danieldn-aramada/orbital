# Contributing

## Setup

Start the local stack (DGraph + PostgreSQL):

```bash
docker compose -f deploy/local/docker-compose.yml up -d
```

See the README for service ports and details.

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
