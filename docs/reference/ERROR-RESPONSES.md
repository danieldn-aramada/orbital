# Error Responses

Read this before: returning any non-2xx JSON from an **orbital or orb** handler, adding a
new REST endpoint, adding a guard/rejection to the GraphQL proxy, or deciding what a
client should `switch` on. This defines the error-response envelope and the `code`
registry. It is a living convention — extend the registry as you add errors.

> Scope: applies to **orbital and orb** (and `orbctl` as a consumer). Orbital adopts first;
> orb follows.
>
> Status: **implemented on orbital** (2026-07-28). Every error returned to Echo on orbital is
> rendered as the envelope below by the central `handler.ErrorHandler` (registered in
> `internal/server`), and the shared GraphQL proxy guards write it via `writeError`. Remaining
> work: orb's own handlers (`internal/orbserver/import_handlers.go`, divergence intake) still
> emit `{"error": ...}` maps / Echo's `{"message": ...}` — that is the "orb follows" step.

Implementation: `internal/handler/errors.go` (`errorResponse`, `writeError`, `ErrorHandler`,
`codeForStatus`, code constants). Related: `internal/handler/graphql.go` (proxy guards + DGraph
pass-through), `docs/reference/AUTH.md` (role enforcement), `docs/reference/DIVERGENCE.md`
(MVCC 409).

## How it's wired (two mechanisms, one shape)

1. **Central `ErrorHandler`** — set as `e.HTTPErrorHandler` on orbital's Echo instance. Every
   returned `error` is rendered as `errorResponse`: an `*echo.HTTPError` keeps its message and
   gets a code from `codeForStatus`; any other (raw/unexpected) error becomes a generic `500
   INTERNAL` with the real error logged against `request.id` and **never** put in the body. This
   retired Echo's default `{"message": ...}` — so a bare `return echo.NewHTTPError(...)` or
   `return err` now speaks the envelope automatically. (It also fixed a latent client bug: the UI
   branches on `if (data.error)`, which silently missed Echo's old `{"message"}` bodies.)
2. **`writeError(c, status, code, msg, hint)`** — direct-writes the envelope. Used where a site
   needs a specific code/hint, or must not depend on the central handler being registered — namely
   the GraphQL proxy guards, which run on **both** orbital and orb. Status line and `httpStatus`
   body field come from one argument and cannot drift.

There is deliberately **no `apiError` type**: `writeError` (explicit code+hint) plus
`echo.HTTPError`→`codeForStatus` (defaults) cover every site. Add one only if a returned (not
written) error ever needs a hint.

## The two envelopes (know which one you're in)

Orbital emits errors in exactly two shapes, by origin:

1. **Orbital-authored errors** — anything orbital itself rejects: REST endpoint
   failures, and the GraphQL proxy's own guard rejections (role check, MVCC conflict).
   These use the **`errorResponse` envelope** defined below.

2. **DGraph-native GraphQL errors** — when a request reaches DGraph and DGraph returns
   a GraphQL `errors[]` array, the proxy passes that body through **untouched**
   (`proxyRaw` / the pass-through write in `Handle`). These follow the GraphQL spec
   (`errors[].message`, `.locations`, `.extensions`) — orbital does not reshape them.

This split is intentional and, for now, permanent. See "Deferred: envelope unification".

## The `errorResponse` envelope

All orbital-authored error responses use this DTO:

```go
// errorResponse is the standard error body for orbital-authored (non-DGraph)
// errors. Swagger-documented; external services consume it.
type errorResponse struct {
	Error      string `json:"error"            example:"dev or admin role required for mutations"`
	Code       string `json:"code"             example:"FORBIDDEN"`
	HTTPStatus int    `json:"httpStatus"       example:"403"`
	Hint       string `json:"hint,omitempty"   example:"Ask an admin to grant you the dev role."`
	DocURL     string `json:"docUrl,omitempty"`
}
```

| Field    | Type   | Required | Semantics |
|----------|--------|----------|-----------|
| `error`      | string | **yes**  | Human-readable message. Safe to surface in the UI. Present tense, no trailing period required. |
| `code`       | string | **yes**  | Stable machine identifier, UPPER_SNAKE. This is what clients branch on — never parse `error`. See registry. |
| `httpStatus` | int    | **yes**  | Mirrors the HTTP status on the response line (e.g. `403`). A convenience/robustness copy — see below. Named `httpStatus`, not `status`, to avoid colliding with orbital's job/operation `status` (the export API's `{"status":"completed"}`). |
| `hint`       | string | no       | Actionable remediation for a human. Omit if `error` already says everything. |
| `docUrl`     | string | no       | **Reserved — not populated today** (orbital serves no docs host yet). Kept in the DTO for when docs are hosted. |

**Two numbers-vs-strings rules, kept separate:**

- **The HTTP status is always on the response status line** (`403`, `409`, `400`) — that's
  the transport, present on every response, read by clients as `response.status`.
- **`httpStatus` (int) in the body mirrors that number.** We include it as a convenience so the
  code survives contexts where the status line is dropped (logs, queues) and so a client has
  everything in one object. Precedent: Google Cloud (`code`), Kubernetes (`code`), RFC 9457
  (`status`), Twilio (`status`). We name ours `httpStatus` (not the bare `status` those APIs
  use) because orbital already uses `status` for job/operation state — see the field table.
- **`code` (string) is the machine identifier clients branch on** — deliberately *not* the
  number. The HTTP status is coarse (many distinct failures are all `400`/`403`); the string
  `code` is specific and stable even if we later change which HTTP status we return. Precedent:
  Stripe (`code:"card_declined"`), Apollo (`extensions.code:"FORBIDDEN"`), Google
  (`status:"PERMISSION_DENIED"`), Kubernetes (`reason:"NotFound"`).

So a rejection carries **both**: `httpStatus: 403` (the number) and `code: "FORBIDDEN"` (the token).

## `code` registry

Codes are UPPER_SNAKE, unique across orbital, and **stable once shipped** (clients depend
on them). Namespace only when a bare word would collide. Add a row here when you add a code.

| `code` | HTTP status | Where raised | Meaning |
|--------|-------------|--------------|---------|
| `FORBIDDEN`                  | 403 | `graphql.go` mutation guard; REST `RequireRole` | Caller authenticated but lacks the required role (dev+ for mutations). |
| `UNAUTHENTICATED`            | 401 | auth middleware | No valid identity presented. |
| `MVCC_CONFLICT`              | 409 | `graphql.go` `ifVersion` check; `export.go` `Trigger` `expectedContentHash` check | State changed since the caller's read; reload and retry. Two scopes, same semantics: `ifVersion` guards **one record**, `expectedContentHash` guards **a whole DC subgraph** before publish (see `OCI.md` § "Guarded Apply"). Set explicitly via `writeError` (NOT the 409 default) — a bare 409 would collide with `CONFLICT`, which the export endpoint also returns for "already in progress", and clients could not tell "re-review your diff" from "wait and retry". |
| `CONFLICT`                   | 409 | REST job triggers (`backup`/`export`/`restore` already-in-progress) | Non-MVCC state conflict. The `codeForStatus` default for any bare 409. |
| `BAD_USER_INPUT`             | 400 (also 405/422) | REST validators; Echo bind/routing errors; `graphql.go` malformed `ifVersion` | Malformed or missing request fields. The `codeForStatus` default for any bare 4xx without a more specific mapping. |
| `VARIABLE_FORM_REQUIRED`| 400 | `graphql.go` inline-selector guard (Spike 31, live) | A single-entity `update{Kind}` mutation passed its `orbId` or `set` inline instead of as GraphQL variables, so the proxy can't stamp `version`/`updatedAt`/`updatedBy`. Kill switch: `ORBITAL_INLINE_SELECTOR_REJECT=false`. |
| `NOT_FOUND`                  | 404 | REST resource lookups | Named resource does not exist. |
| `CONTENT_TOO_LARGE`          | 413 | `BodyLimit` middleware (`server.go`) | Request body exceeds `ORBITAL_MAX_REQUEST_BODY`. Spelling is RFC 9110's current 413 phrase ("Content Too Large"), not the superseded "Payload Too Large". The `codeForStatus` default for any bare 413. |
| `RATE_LIMITED`               | 429 | `RateLimiter` middleware (`server.go`, opt-in via `ORBITAL_RATE_LIMIT_ENABLED`) | Per-IP request rate exceeded; response carries a `Retry-After` header. Chosen over `THROTTLED`/`RESOURCE_EXHAUSTED` — see "Deriving a new code". The `codeForStatus` default for any bare 429. |
| `UNAVAILABLE`                | 503, 502, 504 | dependency not configured/reachable (registry, signing key, bundler, DC resolve) | A required downstream is unavailable. The `codeForStatus` default for 502/503/504. |
| `INTERNAL`                   | 500 | catch-all | Unexpected/raw error. Message is generic (`Internal Server Error`), real detail logged with `request.id` — never in the body. The `codeForStatus` default for any other 5xx. |

Code spellings are **derived, not invented** — see "Deriving a new `code`" below.

`codeForStatus` (in `errors.go`) is the default status→code map for any error that arrives
without an explicit code — bare `echo.NewHTTPError`, Echo's own 404/405/bind errors. Specific
codes that a bare status can't disambiguate (`MVCC_CONFLICT` vs `CONFLICT` on 409) are set at
the raising site via `writeError`.

## Deriving a new `code` (follow this — don't guess, don't re-research)

When you add an error condition, derive its `code` in this order. This exists so the spelling
question is a lookup, not a research cycle — the alternatives below were already checked
against Apollo, GitHub, Shopify, gRPC/Google, and the RFCs (2026-08-13).

1. **Apollo built-in?** If Apollo Server has a built-in code for the condition, use that exact
   spelling, so our envelope and a future GraphQL envelope converge without a rename. The AS4
   built-ins are: `GRAPHQL_PARSE_FAILED`, `GRAPHQL_VALIDATION_FAILED`, `BAD_USER_INPUT`,
   `PERSISTED_QUERY_NOT_FOUND`, `PERSISTED_QUERY_NOT_SUPPORTED`, `OPERATION_RESOLUTION_FAILURE`,
   `BAD_REQUEST`, `INTERNAL_SERVER_ERROR`. (AS4 *dropped* `FORBIDDEN`/`UNAUTHENTICATED`; we keep
   those on semantic grounds — they match gRPC/Google and AS2/AS3 history.)
2. **Clean HTTP-status mapping?** Use the **current RFC 9110** reason phrase, UPPER_SNAKE — e.g.
   413 → `CONTENT_TOO_LARGE` (RFC 9110), **not** the superseded "Payload Too Large" (RFC 7231),
   **not** Go/Echo's legacy `StatusText` ("Request Entity Too Large", RFC 2616). Match today's
   spec, not what the stdlib happens to emit. This is why `FORBIDDEN`/`NOT_FOUND`/`CONFLICT`
   mirror their current phrases.
3. **HTTP doesn't name the semantic well** (auth, quota, rate limiting)? Prefer an established
   ecosystem semantic code over the bare HTTP phrase, in precedence **GraphQL ecosystem →
   gRPC/Google → HTTP phrase**, and pick the clearest:
   - rate limiting (429) → `RATE_LIMITED` (GitHub GraphQL). Rejected: `THROTTLED` (Shopify),
     `RESOURCE_EXHAUSTED` (gRPC/Google), `TOO_MANY_REQUESTS` (bare HTTP) — valid but less clear.
   - no identity (401) → `UNAUTHENTICATED` (gRPC/Google/AS3), not HTTP's misnomer "Unauthorized".
4. Record the choice **and the alternatives you rejected** in the registry row (or here), so the
   next person doesn't re-run the research. UPPER_SNAKE, unique, stable once shipped.

## HTTP status guidance

- **401** — no/invalid identity. `UNAUTHENTICATED`.
- **403** — valid identity, insufficient role. `FORBIDDEN`.
- **400** — client sent something malformed or unsupported. `BAD_USER_INPUT`, `VARIABLE_FORM_REQUIRED`.
- **404** — named resource absent. `NOT_FOUND`.
- **409** — conflict. `MVCC_CONFLICT` (optimistic-concurrency, set explicitly) or `CONFLICT` (other state conflicts, the default).
- **502/503/504** — a downstream dependency is unavailable. `UNAVAILABLE`.
- **5xx (other)** — server fault. Never leak internals in `error`; log with `request.id` and return `INTERNAL`.

GraphQL note: DGraph-native errors ride on **HTTP 200** with a populated `errors[]` (GraphQL
convention). Orbital-authored proxy guards (role, MVCC) are the exception — they short-circuit
*before* DGraph with a real HTTP status (403/409) and the `errorResponse` body.

## Examples

**Role rejection (proxy guard, 403):**
```json
{ "error": "dev or admin role required for mutations", "code": "FORBIDDEN", "httpStatus": 403,
  "hint": "Ask an admin to grant you the dev role." }
```

**MVCC conflict (409):**
```json
{ "error": "This record was modified by someone else. Please reload and try again.",
  "code": "MVCC_CONFLICT", "httpStatus": 409 }
```

**Inline-selector rejection (Spike 31, 400):** — `error`/`hint` name the caller's actual type (`update{Kind}`), so the hint is copy-pasteable.
```json
{ "error": "updateIdracSettings must pass both orbId and set as GraphQL variables, not inline literals: orbital resolves the row via orbId to bump version, and stamps updatedAt/updatedBy into set — it can't do either against inline values.",
  "code": "VARIABLE_FORM_REQUIRED", "httpStatus": 400,
  "hint": "Rewrite with variables — query: mutation UpdateIdracSettings($orbId: String!, $set: IdracSettingsPatch!) { updateIdracSettings(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } } — variables: { \"orbId\": \"namespace:name\", \"set\": { ...fields to change... } }" }
```

**DGraph-native GraphQL error (passed through unchanged, HTTP 200):**
```json
{ "errors": [ { "message": "input:3: Field \"foo\" is not defined ...",
  "extensions": { "code": "..." } } ], "data": null }
```

## Settled Decisions

- **`code` is a stable UPPER_SNAKE string, never the HTTP status number.** Confirmed against
  Stripe, Apollo, Google AIP-193, Kubernetes, GitHub GraphQL — the string identifier is what
  clients branch on; the HTTP number is coarse and lives elsewhere.
- **`httpStatus` (int) mirrors the HTTP status line into the body.** We surface the number for
  convenience/robustness (logs, queues, everything-in-one-object), following Google/k8s/RFC
  9457. It is a *mirror* of the status line, not a substitute for it, and is distinct from
  `code`. (This reverses an earlier lean of omitting it — we choose to surface it.)
- **The field is `httpStatus`, not `status`.** Orbital already uses `status` to mean
  job/operation state (the export API returns `{"status":"completed"}`). Reusing `status` for
  the numeric HTTP mirror in errors would collide semantically, so we qualify it. `code` needs
  no such qualifier — nothing else in orbital uses a bare `code`.
- **`errorResponse` is the envelope for orbital-authored errors only.** DGraph-native
  `errors[]` passes through untouched — do not reshape it in the proxy.
- **`error`, `code`, `httpStatus` are required; never parse `error`.** `error` is for humans and
  may be reworded freely; `code` is the contract and is stable once shipped.
- **`docUrl` is reserved, not populated.** Orbital serves no docs host today, the repo is
  private, and the GitHub org is mid-migration — a link now would be unstable. Keep the field
  defined; populate it once docs are hosted at a stable URL.
- **Codes are documented here before use.** Adding a code without a registry row is
  incomplete — clients have no way to know it's stable.
- **New codes are derived via "Deriving a new `code`", not invented.** Settled 2026-08-13 after
  checking Apollo / GitHub / Shopify / gRPC-Google / RFC 9110: 413 = `CONTENT_TOO_LARGE` (RFC
  9110's current phrase, not the superseded "Payload Too Large"); 429 = `RATE_LIMITED` (GitHub
  GraphQL, chosen over `THROTTLED` / `RESOURCE_EXHAUSTED`). The rejected alternatives are recorded
  so the spelling question is a lookup, not a re-research — don't relitigate.
- **Existing `graphql.go` bodies are a forward-compatible subset.** The current
  `{"error": "..."}` 403/409 responses satisfy this doc once `code`/`httpStatus` are added;
  the addition is additive and non-breaking.

## Deferred: envelope unification (known debt)

Orbital currently exposes two envelopes: `errorResponse` (orbital-authored) and GraphQL
`errors[]` (DGraph pass-through). A client hitting `/graphql` may get either, depending on
whether the request was rejected by a proxy guard or by DGraph. This is acceptable pre-MVP.

The migration seam, if we later unify: wrap orbital's proxy-guard rejections in a synthetic
GraphQL envelope — `{ "errors": [ { "message": {error}, "extensions": { "code": {code},
"httpStatus": {httpStatus}, "hint": {hint} } } ] }` — so `/graphql` speaks one shape. Because
our `code` spellings already match Apollo's (`FORBIDDEN`, `BAD_USER_INPUT`), no code strings
change; only the outer wrapper does. REST endpoints keep `errorResponse`. Do not build this
until there's a concrete client complaint — it is documented here so the option stays open.

---

Sources: [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html) · [Stripe errors](https://docs.stripe.com/api/errors) · [Google AIP-193](https://google.aip.dev/193) · [Kubernetes Status](https://kubernetes.io/docs/reference/generated/kubernetes-api/) · [Twilio responses](https://www.twilio.com/docs/usage/twilios-response) · [Apollo Server errors](https://www.apollographql.com/docs/apollo-server/data/errors) · [Shopify UserError](https://shopify.dev/docs/api/admin-graphql/latest/objects/UserError) · [Vault API](https://developer.hashicorp.com/vault/api-docs)
