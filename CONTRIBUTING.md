# Contributing

## Setup

Start the local stack (DGraph + PostgreSQL):

```bash
docker compose -f deploy/local/docker-compose.yml up -d
```

See the README for service ports and details.

## Running end-to-end tests

E2E tests use [Playwright](https://playwright.dev/) and run against a live local stack. Before running:

1. Start the local stack and the orbital server:
   ```bash
   docker compose -f deploy/local/docker-compose.yml up -d
   go run ./cmd/orbital
   ```

2. Seed test data into DGraph (via the GraphiQL UI at `http://localhost:8080` or any GraphQL client):
   ```
   examples/colo-galleon.graphql
   ```

3. Run the tests:
   ```bash
   npm run test:e2e
   ```

   Or open the interactive Playwright UI:
   ```bash
   npm run test:e2e:ui
   ```

Tests assume the `colo-galleon` data center is seeded and orbital is running on `http://localhost:8001`.

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
