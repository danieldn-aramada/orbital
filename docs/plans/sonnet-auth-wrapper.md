# Sonnet Auth Wrapper — `scripts/orbital-curl` + `orbctl token`

Status: ready for Sonnet to execute
Owner: Daniel
Date: 2026-06-13

## Goal

Give Sonnet a single primitive — `scripts/orbital-curl` — that calls authenticated orbital/orb APIs without ceremony. The wrapper transparently handles token retrieval, silent refresh, and falls back to running `orbctl login` (which opens a browser on the user's Mac) when re-auth is required.

**Why:** Sonnet is regularly asked to verify changes by running orbital/orb and hitting authenticated endpoints. Today Sonnet has no clean way to obtain a bearer token. Hand-rolled `curl -H "Authorization: Bearer ..."` requires Sonnet to find the token somewhere, which it can't. This plan closes that gap while preserving honest audit attribution (real user email, not `app:<id>`).

**What this is NOT:** a service-credentials pattern. Sonnet acts on the human user's behalf; mutations must be attributed to the user. Do not reach for `client_credentials` or pre-shared service tokens. See ADR 010 §App Caller Authorization — that path is reserved for genuinely autonomous services (bundler), not human-directed agents.

---

## Architecture (1 sentence)

`orbctl token` prints a valid access token to stdout (silently refreshing if needed). `scripts/orbital-curl` calls `orbctl token`, falls back to running `orbctl login` interactively if no refresh is possible, then exec's `curl -H "Authorization: Bearer $TOKEN" "$@"`.

---

## File-by-file changes

### 1. New subcommand: `internal/orbctl/token.go`

Cobra command file alongside `login.go` / `logout.go`. Single responsibility: print a valid access token to stdout, or exit non-zero with a stderr error.

Skeleton:

```go
package orbctl

import (
    "fmt"
    "os"

    "github.com/armada/orbital/internal/orbauth"
    "github.com/spf13/cobra"
)

var tokenCmd = &cobra.Command{
    Use:   "token",
    Short: "Print the current access token (refresh silently if needed)",
    Long: `Prints the current orbital access token to stdout. If the cached token
is expired, transparently refreshes using the stored refresh token. If no
refresh is possible (no session, refresh token expired), exits non-zero with
a message suggesting 'orbctl login'.

Intended for shell scripting:

    curl -H "Authorization: Bearer $(orbctl token)" ...

Stdout is the token only — no trailing newline beyond what shells will trim.
Logging and human messages go to stderr.`,
    SilenceUsage: true,
    RunE: func(cmd *cobra.Command, args []string) error {
        creds, err := orbauth.GetCredentials()
        if err != nil {
            return err
        }
        fmt.Fprint(os.Stdout, creds.AccessToken)
        return nil
    },
}

func init() {
    rootCmd.AddCommand(tokenCmd)
}
```

Use `fmt.Fprint` (no trailing newline) so `$(orbctl token)` is the exact token without whitespace pollution. Cobra's error printing already routes to stderr.

**Do NOT:** add a `--refresh` flag, a `--quiet` flag, or any token-format options. The command does one thing.

### 2. New script: `scripts/orbital-curl`

Create the directory if missing (`mkdir -p scripts`). The file is bash, executable (`chmod +x`):

```bash
#!/usr/bin/env bash
# orbital-curl — authenticated curl against orbital.
#
# Usage: scripts/orbital-curl [curl args...]
#
# Reads token from `orbctl token`. If no valid session exists, automatically
# runs `orbctl login` (which opens a browser on the user's machine). After
# login, retries token retrieval.
#
# All audit attribution is to the real user identity. Do NOT replace this with
# client_credentials — see ADR 010.

set -euo pipefail

get_token() {
    orbctl token 2>/dev/null
}

token="$(get_token || true)"

if [[ -z "$token" ]]; then
    echo "orbital-curl: no valid token — running 'orbctl login'..." >&2
    echo "orbital-curl: a browser will open on this machine; complete the sign-in." >&2
    orbctl login >&2
    token="$(get_token)"
fi

if [[ -z "$token" ]]; then
    echo "orbital-curl: failed to obtain token after login attempt" >&2
    exit 1
fi

exec curl -H "Authorization: Bearer ${token}" "$@"
```

**Do NOT:** add a `--no-login` flag, a fallback to client_credentials, or any env-var-controlled toggle. The script does one thing: get a user token, use it. If you find yourself wanting "what if Sonnet runs unattended overnight" — that's a separate use case, not this script.

### 3. Tests

#### `internal/orbctl/token_test.go` (new)

Two cases:

1. **`TestTokenCmd_PrintsValidToken`** — set up an in-memory store via the same pattern `getCredentialsFromStore` uses in `orbauth_test.go`. Inject credentials with a future `ExpiresAt`. Capture stdout. Assert it equals `creds.AccessToken` exactly with no trailing newline.

2. **`TestTokenCmd_NoSession_ReturnsError`** — empty store. Assert `cmd.Execute()` returns a non-nil error and the error string contains "orbital login" (the suggestion text from `orbauth`).

If the existing orbctl tests already wire a Cobra root with stubbed deps, follow that pattern. If not, you can call `tokenCmd.RunE(tokenCmd, nil)` directly and capture via `os.Pipe` for stdout. Don't introduce a new mocking framework — use what `root_test.go` already does.

#### Manual integration test (no automated CI for this)

After implementing, from a shell:

```bash
go run ./cmd/orbctl token   # should print token if logged in, else error
go run ./cmd/orbctl logout
go run ./cmd/orbctl token   # should print "session expired — run 'orbital login'"
scripts/orbital-curl http://localhost:8001/api/v1/inventory  # should auto-trigger orbctl login
```

The third command should pop a browser, you click through, then `curl` runs against `/api/v1/inventory` with the new token. Verify the audit log shows your email, not `app:...`.

### 4. Documentation

#### `CLAUDE.md` — Settled Decisions

Add this exact line under Settled Decisions:

```
- **Sonnet hits authenticated orbital/orb endpoints via `scripts/orbital-curl`** — wraps `orbctl token` for silent refresh and auto-triggers `orbctl login` (browser-based) when refresh fails. Audit attribution stays as the real user. Do NOT hand-roll `curl -H "Authorization: Bearer ..."` for authenticated endpoints — the wrapper exists so the token plumbing has one place. Do NOT use `client_credentials` for human-directed verification; that pattern is for autonomous services only (bundler — see ADR 010). The wrapper assumes the user is at the machine (browser pop-up requires interaction); for unattended runs, no path exists by design.
```

#### `docs/reference/AUTH.md` — orbital-cli section

Find the section describing orbital-cli subcommands. Add an entry for `orbctl token` between `login` and `logout`:

```
- **`orbctl token`** — prints a valid access token to stdout. Silently refreshes if expired. Exits non-zero with a stderr message if no session exists (or refresh is impossible). Intended for shell scripting: `curl -H "Authorization: Bearer $(orbctl token)" ...`. Use `scripts/orbital-curl` instead of building this command line by hand.
```

In the same file, add a new subsection at the bottom:

```
## Sonnet / dev workflow

When Sonnet needs to verify behavior by hitting an authenticated orbital or orb endpoint, it uses `scripts/orbital-curl` instead of `curl`. The wrapper:

1. Calls `orbctl token` for a fresh access token (refreshes silently if expired).
2. If no refresh is possible, runs `orbctl login` (browser opens on the user's machine; user clicks through).
3. Exec's `curl` with the bearer token.

Refresh-token TTL is Azure AD's default (~14 days, depending on tenant policy), so re-login happens roughly every two weeks of active use. The audit log always reflects the real user identity. There is no fallback to `client_credentials` by design — see ADR 010 §App Caller Authorization for why human-directed agents must not use service-principal credentials.
```

### 5. Memory entry (for Sonnet to find in future sessions)

Add to memory:

```markdown
---
name: sonnet-auth-orbital-curl
description: Sonnet uses scripts/orbital-curl (not raw curl) for authenticated orbital/orb endpoints; wrapper handles tokens + login
metadata:
  type: feedback
---

When verifying orbital/orb changes by hitting authenticated endpoints, Sonnet must use `scripts/orbital-curl` instead of raw `curl`. The wrapper calls `orbctl token` for silent refresh and auto-triggers `orbctl login` (browser-based) when refresh fails.

**Why:** Audit attribution requires the real user identity. Service-principal / client_credentials paths attribute mutations to `app:<id>` rather than the user — wrong for human-directed verification work (right only for genuinely autonomous services like the bundler). See [[project_enricher_architecture]] and ADR 010.

**How to apply:**
- Every time Sonnet needs `curl http://localhost:8001/api/v1/...` or `curl http://localhost:8010/api/v1/...` (orbital or orb), use `scripts/orbital-curl /api/v1/...` instead.
- If the wrapper fails because no orbital server is running, that's a separate problem — start it first via `make run-orbital`.
- If a browser pops up during a Sonnet curl, that's expected (refresh expired, ~once every two weeks). User completes the click-through and the curl retries automatically.
- Do NOT introduce a `client_credentials` fallback or a static dev token to make this "more autonomous." The friction is the feature — see CLAUDE.md Settled Decisions.
```

Add a corresponding index line to `MEMORY.md`:

```
- [scripts/orbital-curl for authenticated dev calls](feedback_sonnet_auth_orbital_curl.md) — use the wrapper, never raw curl with bearer; preserves real-user audit attribution
```

---

## Order of operations for Sonnet

1. Implement `internal/orbctl/token.go` (§1).
2. Run `go build ./...` to confirm orbctl still compiles.
3. Add `internal/orbctl/token_test.go` (§3).
4. Run `go test ./internal/orbctl/...` to confirm.
5. Create `scripts/orbital-curl` (§2). `chmod +x scripts/orbital-curl`.
6. Manual smoke: `go run ./cmd/orbctl token` while logged in. Then `make run-orbital` in one terminal, `scripts/orbital-curl http://localhost:8001/api/v1/inventory` in another.
7. Update `CLAUDE.md` and `docs/reference/AUTH.md` (§4).
8. Write memory entry (§5).

---

## Anti-patterns to avoid

- **Do NOT add a client_credentials fallback** to `scripts/orbital-curl`. The pattern from ADR 010 exists for autonomous services. Sonnet is human-directed; mutations under `app:<id>` muddy the audit log permanently. If you find yourself thinking "but what about unattended runs," that's a different script and a different ADR — write it separately.
- **Do NOT introduce a static dev API token** as an alternative to `orbctl login`. Explicitly rejected in ADR 010 §Options Considered §A.
- **Do NOT add a `--no-login` flag** that suppresses the auto-`orbctl login` trigger. The whole point of the wrapper is to be invisible to Sonnet. If you want a no-browser variant for CI, that's a different tool with explicit scope.
- **Do NOT bake the orbital URL** into the wrapper. Sonnet passes the full URL just like with `curl`. The wrapper only adds the bearer header.
- **Do NOT use keychain or env-var token caching outside what `orbauth` already does.** Token storage and refresh are `orbauth`'s job; the new subcommand is a thin printer on top.
