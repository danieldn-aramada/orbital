# 004 — Security Logging Alignment (OWASP / NIST)

**Status:** Decided

**Date:** 2026-06-05

---

## Decision

Align Orbital's logging with OWASP Logging Cheat Sheet and NIST SP 800-92 / SP 800-53 AU-3. Two complementary log streams serve different purposes; neither replaces the other.

---

## Two-Stream Model

| Stream | Location | Purpose | Audience | Retention |
|--------|----------|---------|----------|-----------|
| **Access log** | Echo `RequestLogger` (stdout JSON) | Operational observability — what hit the server, how fast, what failed | On-call engineer | Days to weeks |
| **Audit log** | PostgreSQL `events` table | Accountability — who changed what, when, what was the outcome | Security review, incident investigation, compliance | Months to years |

### Access Log Fields (per request)

```json
{"time":"...","level":"INFO","msg":"request","method":"PUT","uri":"/api/v1/users/4/role","status":200,"latency_ms":8,"actor":"alice@armada.ai"}
```

- `actor` — user email from Echo context (`c.Get("user_email")`). Set by the session middleware on every request. Empty string for unauthenticated requests.
- Static assets (`/static/*`, `/favicon.ico`) and the device code poll endpoint (`/auth/device/poll`) are suppressed from the access log — zero signal, high volume.

### Why `actor` in Both Streams

The access log is the safety net. If a handler has a bug and never reaches the `writeAuditEvent` call, the access log still records who made the request. This is the defense-in-depth principle from OWASP: "if your audit trail has gaps, the access log fills them."

---

## OWASP / NIST Alignment

| OWASP Requirement | Implementation |
|---|---|
| WHO performed the action | `actor` field in access log + `actor` column in `events` table |
| WHAT happened | `method`+`uri` in access log; `operations[]` + `details` in audit log |
| WHEN it happened | `time` field (RFC3339) |
| Authentication successes | `loginSuccess` audit event — local login, OIDC callback, device code |
| Authentication failures | `loginFailed` audit event — wrong password, unknown email, expired device code |
| Session events | `logout` audit event |
| Access control failures | `authorizationDenied` audit event + `slog.Warn` in `RequireRole` middleware |

NIST SP 800-53 AU-3 requires: event type, date/time, where, source, outcome, identity of subjects. All fields are present across the two streams combined.

---

## Audit Events: Authentication and Authorization

All auth/authz events use `event_category: "management"`. Operation names follow the `verbNoun` camelCase convention (ADR 003).

| Operation | Trigger | Actor |
|---|---|---|
| `loginSuccess` | Successful local login, OIDC callback, or device code auth | Verified email |
| `loginFailed` | Wrong password, unknown email, expired device code | Attempted email (local) or `""` (SSO/device — email not yet verified at failure point) |
| `logout` | `POST /user/logout` with valid CSRF | Email from session (read before clearing) |
| `authorizationDenied` | `RequireRole` middleware rejects a mutating request | Email of the rejected user |
| `updateUserRole` | `PUT /api/v1/users/:id/role` succeeds | Admin who made the change |

**Note on `loginFailed` actor for SSO/device code:** The actor is the *attempted* email for local auth (what the user typed). For OIDC and device code failures, the email is not yet verified at the point of failure, so actor is `""`. OWASP recommends logging the attempted identity, not a verified one — both behaviors are correct for their respective failure modes.

---

## What We Do NOT Log

- **Request bodies** — may contain passwords (`/user/login`) or device codes (`/auth/device/poll`). The access log is metadata-only.
- **Raw JWTs or tokens** — credential leak. The OIDC handler previously logged the raw ID token at INFO level for debugging; this was removed as a security fix.
- **Client IP / X-Forwarded-For** — behind Istio, the source IP is always the sidecar proxy. Istio's own access log captures the real client IP. Adding it to the application log would be misleading.
- **Health checks and polling endpoints** — suppressed via Echo `Skipper` (zero security signal, high volume).
- **Sensitive reads (GET /api/v1/users)** — access log with actor covers this for MVP. Post-MVP: add a `listUsers` management audit event if compliance requires it.

---

## Files Affected

| File | Change |
|---|---|
| `internal/server/server.go` | Added `actor` field to `LogValuesFunc`; suppressed `/auth/device/poll` from Skipper |
| `internal/handler/authz.go` | `RequireRole` logs `slog.Warn` + writes `authorizationDenied` audit event on 403 |
| `internal/handler/login.go` | Added `logger *slog.Logger`; writes `loginSuccess`, `loginFailed`, `logout` audit events |
| `internal/handler/oidc.go` | Removed raw JWT log; writes `loginSuccess` (Callback + DeviceCodePoll success), `loginFailed` (DeviceCodePoll expired/error) |

---

## Related

- `docs/decisions/001-mutation-audit-recording.md` — GraphQL mutation audit events
- `docs/decisions/003-audit-event-categories.md` — `"data"` vs `"management"` event categories
- `docs/reference/AUTH.md` — auth architecture (OIDC, device code, sessions)
