# Plan: Rename `orbital-cli` → `orbctl`

## Background and goal

We are renaming the CLI from `orbital-cli` to `orbctl`. Two things make this confusing today and motivate the rename:

1. **Source directory says `orbital-cli`** (`cmd/orbital-cli/`, `internal/orbital-cli/`) — but the **released binary is named `orbital`** (`scripts/release-cli.sh` ships `dist/orbital`, the Homebrew formula does `bin.install "orbital"`, and cobra's root command has `Use: "orbital"`). So the source name and the distributed name already disagree.
2. The distributed name `orbital` **collides with the cloud server's product name**, which is also called "Orbital." When a developer reads `orbital` in docs they cannot tell whether it's the binary or the daemon.

The new name `orbctl` follows industry convention (kubectl, etcdctl, istioctl, crictl) and unambiguously distinguishes the CLI from both daemons (`orbital`, `orb`).

**Settled decisions confirmed before this work:**
- One CLI long-term, not two. `orbctl` targets any orbital-API-speaking server — cloud or edge. Future auth divergence is handled via context profiles, not a separate binary.
- Hard cutover (no `orbital-cli` alias retained). This is early-MVP so backward-compat shims aren't worth carrying.
- Tag scheme stays `cli/v*` (don't churn tag history; the prefix is abstract).

## What "done" looks like

- `go build ./cmd/orbctl` produces a binary; `cmd/orbital-cli/` is gone.
- `make build-orbital-cli` is removed; `make build-orbctl` replaces it.
- `bin/orbctl --help` shows `Usage: orbctl [command]`.
- `bin/orbctl version` prints the injected version.
- The release script ships `orbctl_<version>_<os>_<arch>.tar.gz` and updates a `Orbctl` Homebrew formula.
- All docs (READMEs, AUTH.md, CHANGELOG.md, ADRs, ROADMAP, CLAUDE.md) refer to `orbctl`, never `orbital-cli` or "the orbital CLI binary."
- Existing tests pass (`make test-unit` minimum; full regression preferred).
- Settled decision recorded in CLAUDE.md.

## Pre-flight checks

Before starting, verify the current state:

```bash
# Confirm scope
grep -rln "orbital-cli" --include="*.go" --include="*.md" --include="Makefile" --include="*.sh" --include="*.yaml" .

# Confirm no uncommitted work that would conflict
git status

# Confirm tests currently green (do not start with broken baseline)
make test-unit
```

If `make test-unit` fails before you start, stop and report — don't conflate test failures with this rename.

## Execution steps

### 1. Rename source directories with `git mv` (preserve history)

```bash
git mv cmd/orbital-cli cmd/orbctl
git mv internal/orbital-cli internal/orbctl
```

### 2. Update Go import paths

Search and replace **only the import path strings**:

```bash
# Files affected (already known): cmd/orbctl/main.go uses an aliased import
grep -rln "github.com/armada/orbital/internal/orbital-cli" --include="*.go" .
```

For each match, replace `github.com/armada/orbital/internal/orbital-cli` → `github.com/armada/orbital/internal/orbctl`. The aliased import in `cmd/orbctl/main.go` currently reads:

```go
import orbitalcli "github.com/armada/orbital/internal/orbital-cli"
```

Change to:

```go
import orbctl "github.com/armada/orbital/internal/orbctl"
```

Then update any usage in that file from `orbitalcli.X` to `orbctl.X`.

### 3. Update cobra `Use` field

In `internal/orbctl/root.go`:

```go
// before
Use: "orbital",

// after
Use: "orbctl",
```

Do NOT change subcommand `Use:` strings (`get`, `login`, `logout`, `patch`, `version`, etc.) — those are subcommands and remain the same.

### 4. Update DefaultServerURL doc comment

In `internal/orbctl/get_dc.go` around line 19–23, the comment references the old ldflag path:

```go
//	-X 'github.com/armada/orbital/internal/orbital-cli.DefaultServerURL=...'
```

Update to:

```go
//	-X 'github.com/armada/orbital/internal/orbctl.DefaultServerURL=...'
```

### 5. Update Makefile

In `Makefile`:

```makefile
# .PHONY line — replace `build-orbital-cli` with `build-orbctl`
.PHONY: help build build-orbital build-orbctl build-orb run-orbital push test ...

# Build target — replace the entire build-orbital-cli target
build-orbctl: ## Build the orbctl CLI → bin/orbctl
	go build $(CLI_LDFLAGS) -o $(BIN_DIR)/orbctl ./cmd/orbctl
```

Verify `CLI_VERSION` derivation still works (it uses `cli/v*` tags — keep as is):

```makefile
CLI_VERSION ?= $(shell (git describe --tags --match 'cli/v*' --dirty 2>/dev/null || echo "cli/v0.0.0-dev") | sed 's|^cli/||')
```

Confirm `CLI_LDFLAGS` references the new package path. If it currently injects into `internal/orbital-cli.DefaultServerURL`, update to `internal/orbctl.DefaultServerURL`. (Check by running `grep -n CLI_LDFLAGS Makefile`.)

### 6. Update release script

In `scripts/release-cli.sh`:

**a.** LDFLAGS package path:
```bash
# before
LDFLAGS="-s -w -X github.com/armada/orbital/internal/version.Version=$VERSION -X 'github.com/armada/orbital/internal/orbital-cli.DefaultServerURL=...'"

# after
LDFLAGS="-s -w -X github.com/armada/orbital/internal/version.Version=$VERSION -X 'github.com/armada/orbital/internal/orbctl.DefaultServerURL=...'"
```

**b.** Build output path:
```bash
# before
GOOS=darwin GOARCH=$arch go build -ldflags "$LDFLAGS" -o dist/orbital ./cmd/orbital-cli

# after
GOOS=darwin GOARCH=$arch go build -ldflags "$LDFLAGS" -o dist/orbctl ./cmd/orbctl
```

**c.** Archive name and tar input:
```bash
# before
tar -C dist -czf "dist/orbital_${VERSION}_darwin_${arch}.tar.gz" orbital
rm dist/orbital

# after
tar -C dist -czf "dist/orbctl_${VERSION}_darwin_${arch}.tar.gz" orbctl
rm dist/orbctl
```

**d.** SHA file references (the `dist/orbital_${VERSION}_..._${arch}.tar.gz` paths) — update everywhere to `orbctl_*`.

**e.** Homebrew formula generation — class name, install line, test line:
```ruby
# before
class Orbital < Formula
  desc "Orbital CLI — authenticate and interact with the Orbital cloud service"
  ...
  url ".../orbital_${VERSION}_darwin_arm64.tar.gz"
  ...
  def install
    bin.install "orbital"
    generate_completions_from_executable(bin/"orbital", "completion")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/orbital version")
  end
end

# after
class Orbctl < Formula
  desc "orbctl — CLI for the Orbital configuration management system"
  ...
  url ".../orbctl_${VERSION}_darwin_arm64.tar.gz"
  ...
  def install
    bin.install "orbctl"
    generate_completions_from_executable(bin/"orbctl", "completion")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/orbctl version")
  end
end
```

**f.** Formula file path inside the tap repo:
```bash
# before
cp /tmp/orbital.rb "$TMP/tap/Formula/orbital.rb"
( cd "$TMP/tap" && git add Formula/orbital.rb && git commit -m "orbital ${VERSION}" && git push )

# after
cp /tmp/orbctl.rb "$TMP/tap/Formula/orbctl.rb"
( cd "$TMP/tap" && git add Formula/orbctl.rb && git commit -m "orbctl ${VERSION}" && git push )
```

**g.** Echo messages at the bottom — update install/upgrade hints from `brew install orbital` to `brew install orbctl`.

Note: The OLD `Formula/orbital.rb` will remain in the homebrew tap repo (we don't touch the tap from here). After the first `orbctl` release ships, the user will manually delete `Formula/orbital.rb` from the tap and bump tap README docs. **Add a step to the report** flagging that the tap repo still has a stale `Formula/orbital.rb`.

### 7. Documentation updates

The following files reference `orbital-cli` and must be updated. Use `git grep -l "orbital-cli"` to be sure none are missed.

#### a. CLAUDE.md

Find any references to `orbital-cli`. Replace with `orbctl`. Specifically the per-component versioning settled decision:

> **Per-component versioning across binaries** — server tags as `v*` (existing lineage), orbital-cli as `cli/v*`, orb as `orb/v*`.

Change to:

> **Per-component versioning across binaries** — server tags as `v*` (existing lineage), orbctl as `cli/v*`, orb as `orb/v*`.

Add a new settled decision under "Settled Decisions":

> **CLI binary is `orbctl`, source lives in `cmd/orbctl/` and `internal/orbctl/`** — follows the `kubectl`/`istioctl`/`etcdctl` convention. Distinguishes the CLI from the `orbital` cloud daemon and the `orb` edge daemon, both of which would collide with shorter names. One CLI targets both — there is no per-app CLI. Future auth divergence (e.g. orb gaining its own IdP) is handled via context profiles in `orbctl`, not a forked binary.

#### b. ROADMAP.md

Replace `orbital-cli` with `orbctl` everywhere.

#### c. CHANGELOG.md

Add a new entry at the top:

```markdown
## CLI

- **Renamed CLI binary from `orbital` to `orbctl`** (and source paths from `cmd/orbital-cli/` to `cmd/orbctl/`). The binary name now matches the kubectl/istioctl convention and no longer collides with the `orbital` cloud daemon name. Existing users on `brew install orbital` should switch to `brew install orbctl` once the new tap formula lands.
```

Also fix any in-place references to `orbital-cli` in older entries.

#### d. docs/auth.md and docs/reference/AUTH.md

Replace `orbital-cli` with `orbctl` everywhere. Update any code blocks that show `orbital-cli ...` invocations to `orbctl ...`. Also check if AUTH.md describes the `orbital login` flow — update example commands to `orbctl login`.

#### e. docs/decisions/

- **`004-namespace-id-scalar-migration.md`** — replace `orbital-cli` with `orbctl` (single hit).
- **`009-per-component-versioning.md`** — replace `orbital-cli` with `orbctl`. Keep the `cli/v*` tag convention as-is (rationale: tag history continuity). Add a footnote noting the binary rename happened in a later commit but the tag scheme was retained.

#### f. docs/findings/maintainability.md and docs/project-background.md

Replace `orbital-cli` with `orbctl`.

#### g. Root README.md

Search the repo root README for any mention of the CLI (install instructions, usage examples). Update. If no README exists at the root, skip — but verify with `ls README.md`.

### 8. Verify nothing slipped

```bash
# These should return ZERO results after the rename:
grep -rn "orbital-cli" --include="*.go" --include="*.md" --include="Makefile" --include="*.sh" --include="*.yaml" --include="*.yml" --include="Dockerfile*" .

# These should also be ZERO:
grep -rn "cmd/orbital-cli\|internal/orbital-cli" .
grep -rn "bin/orbital-cli" .

# And the old Cobra Use string should be gone in CLI source:
grep -rn 'Use:\s*"orbital"' internal/orbctl/
```

If any of the above return hits, fix them before proceeding. The grep over the whole repo is the authoritative "are we done with the rename" check.

### 9. Build and smoke

```bash
# Compile
make build-orbctl

# Confirm the binary works
./bin/orbctl --help
./bin/orbctl version

# Confirm import path resolved
go vet ./...
go build ./...
```

### 10. Run regression suite

```bash
make test-unit

# Optional but recommended:
make up
make test-integration
# (e2e tests are unrelated to the CLI; skip unless you suspect coupling)
```

If unit tests fail, the most likely cause is a missed import or a test that hardcoded the old package path. Search and fix.

### 11. Update the user's local install (manual, outside this plan's scope)

After the next release script run, the user will:
1. `brew uninstall orbital`
2. `brew install orbctl` (once the new formula is in the tap)
3. `brew untap` the old formula path if it lingers
4. Delete `Formula/orbital.rb` from the homebrew tap repo

**This plan does not perform those steps** — the homebrew tap is a separate repo. Flag this in the final report so the user does the tap cleanup manually after the next CLI release.

## What NOT to touch

- **Tag scheme stays `cli/v*`** — do not rename existing tags or move to `orbctl/v*`. Continuity matters more than naming purity here.
- **Subcommand names** (`get`, `login`, etc.) are fine as-is — they're already conventional.
- **Cosign keys, OCI signing, schema files** — unrelated to this rename.
- **No new tests are required** for the rename itself. Existing tests assert command behavior, not binary name.

## Rollback plan

If the rename causes unexpected breakage:

```bash
git restore --staged --worktree .
# or, if already committed:
git reset --hard HEAD~1
```

The rename is mechanical — there should be no semantic change to behavior. If tests fail after the rename, the cause is a typo in a `sed`-style replacement, not a logic issue. Read the error, fix in place, re-run.

## Final report should include

1. List of files changed and renamed (with `git status` output)
2. Output of `bin/orbctl --help` (proves the rename took effect end-to-end)
3. Output of `make test-unit` (must be green)
4. Confirmation that the `grep -rn "orbital-cli"` sweeps return zero hits in tracked source
5. Reminder to the user that the homebrew tap repo (`danieldn-aramada/homebrew-tools`) still has a stale `Formula/orbital.rb` that needs manual deletion after the next CLI release
6. Suggested commit message:

   > `Rename CLI binary from orbital/orbital-cli to orbctl`
   >
   > One CLI binary targets both orbital and orb (same GraphQL API). New
   > name follows kubectl/istioctl convention and no longer collides with
   > the orbital cloud daemon. Tag scheme stays cli/v*.

   Do NOT commit — the user always commits themselves.
