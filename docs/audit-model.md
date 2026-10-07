# Orbital's audit model

**Read this before:** answering an auditor's question about orbital's audit trail, writing an SSP
narrative for the AU control family, or deciding what orbital can and cannot tell you about who
changed what. Engineers changing audit code want `docs/reference/AUDIT.md` instead — this document
explains the model; that one is the implementation reference.

**Status key used throughout.** ✅ shipped · 🟡 partial · 📋 designed, not built · ➖ not addressed.
**Every claim below carries one.** Where orbital does not do something, this document says so — a
compliance document that overstates is worse than none, because someone will cite it.

**Control references are NIST SP 800-53 Rev. 5 / FedRAMP.** Orbital is not itself FedRAMP
authorized; it is a component an adopter deploys inside their own boundary. Nothing here asserts a
compliance posture on an adopter's behalf. Cryptographic module scope is deliberately out of
scope for this document — see the SDD for that.

---

## 1. What the audit log is for

Orbital is an intent-only CMDB and is never in the reconciliation path, so the only thing it can be
authoritative about is what happened **at its own API boundary**:

> **Provenance for control-plane actions — every act that changed orbital's state, plus every
> authentication and authorization failure, and what each carried.**

It answers **who did what, in one act, and when**. It does not answer *what is the state now* (that
is the Topology API) or *what is the net difference between two points* (that is the export
preview's content diff). Those are different questions with different stores, deliberately.

### What gets a record — two criteria, and only two

1. **State changed.** A mutation committed, a policy was created or deleted, a backup ran.
2. **A security event occurred.** An authentication or authorization failure.

### What deliberately gets no record

Auditors ask about absences, so these are stated rather than omitted:

- **Business-logic refusals.** A write refused because it needs approval first produces no audit
  row — the caller is present and holds the error, and the durable record is the write that
  eventually lands. These are written to the application log instead.
- **Technical failures.** A mutation rejected by the graph database leaves no row.
- **Reads.** Orbital audits mutations and security events, not queries.
- **Requests carrying no credential at all.** A rejected *bearer token* is recorded
  (`bearerAuthFailed`, with the reason: untrusted issuer, verification failure, audience mismatch,
  unreadable claims, no usable identity, no role mapping, malformed token). A request that presents
  **no** credential is not — it is an unauthenticated request rather than an authentication failure,
  it fires for every anonymous probe, and the access log already carries the 401.

This follows NetBox (change records only on commit) and GitHub (a rejected push is an error, not an
audit entry). It differs from AWS CloudTrail, which records failed calls — a deliberate divergence,
not an oversight.

**Consequence worth understanding:** "how many writes were blocked pending review" is **not**
answerable from the audit log. That is an application-log question.

---

## 2. What a record contains

One event per HTTP request. A compound mutation touching several entities produces one row with
several resources attached.

| Field | Meaning |
|---|---|
| `id` | Event identifier (UUID) |
| `timestamp` | When it happened, UTC |
| `actor` | Who — email where known, else display name, else `app:<client-id>` for a service principal, or a `system:*` label for background work |
| `acting_client` | The OAuth client that made the call, recorded **only when it differs from the subject** — i.e. a trusted upstream service acting for a human |
| `event_category` | `data` (intent changed), `management` (system operation), `auth` (login/logout/failure) |
| `event_source` | Where it entered — `graphql`, `rest`, `internal`, and for some auth paths `bearer` / `oidc` |
| `source_ip_address` | Caller address; absent for background jobs, which have no request |
| `request_id` | Ties every event from one HTTP request together |
| `operations` | What was done, e.g. `["updateServer"]` or `["createBackup"]` |
| `resource_types` / `resource_ids` | What was touched, by type and by stable identifier (`orbId`) |
| `details` | The full payload: the mutation document, its variables, and a partial before-image |
| `authorization` | The act that authorized the write — a merged change request, an accepted divergence. A record-metadata field like `actor` and `eventSource`, **not** part of the mutation payload. **Present only when one exists** |

### Under what authority — three states, and the absence is meaningful

| State | Marker | Means |
|---|---|---|
| **Reviewed** | `authorization: {type, id}` | The write passed a control. Two types exist today: `changeRequest` (`id` = the human identifier, e.g. `colo-36`, resolvable at `GET /api/v1/change-requests/colo-36`) and `divergenceResolution` (`id` = the divergence entry's uuid) |
| **Break-glass** | `privileged` + `bypassedPolicy` | A control applied and the caller's role let them skip it |
| **Ungated** | neither | No control applied to this write |

`authorization` carries the human identifier (`colo-58`), resolvable through
`GET /api/v1/change-requests/colo-58` for the author, approvers and comments. The audit log answers
*which* control authorized a change; it does not answer *who approved it* — `actor` is whoever
merged. That split is deliberate: the change-request lifecycle lives in the approval tables and
emits no audit events (`CHANGE-CONTROL.md`, settled 2026-09-17).

⚠️ **Neither marker does not prove nothing was gated.** An event with no `authorization` and no
`privileged` means no policy covered that write — *or* that change control was switched off entirely,
because the gate returns early in both cases and leaves the same trace. Orbital does not yet record
whether a control was evaluated, only the notable outcomes. Tracked in `docs/planning/debt.md`; the
intended fix follows Kubernetes, which records an authorization decision on every request rather than
only on exceptions.

⚠️ **`authorization` is absent on every event written before 2026-10-07, regardless of provenance.**
Audit rows are append-only in the database, so historical events could not be annotated and never
will be. An absence on an older event means "unknown", not "not reviewed".

### Mapping to AU-3 (Content of Audit Records)

| AU-3 requires | Orbital | Status |
|---|---|---|
| a. What type of event occurred | `operations` + `event_category` | ✅ |
| b. When the event occurred | `timestamp` | 🟡 see §7 — the API emits second granularity |
| c. Where the event occurred | `source_ip_address`, `event_source`, `resource_ids` | ✅ |
| d. Source of the event | `actor`, `acting_client` | ✅ |
| e. Outcome of the event | **Implicit.** A `data` or `management` row exists only on success; `auth` rows and authorization refusals carry a `reason` in `details` | 🟡 |
| f. Identity of individuals/entities involved | `actor` + `resource_ids` | ✅ |

**(e) deserves the caveat.** Because refusals are not recorded (§1), the presence of a row *is* the
success signal. There is no `outcome` column. An auditor expecting an explicit success/failure field
will not find one, and should be told why.

### Mapping to AU-3(1) (Additional Audit Information)

FedRAMP's assigned list, item by item:

| Required | Orbital | Status |
|---|---|---|
| Session / transaction / activity duration | Not recorded | ➖ |
| Bytes sent and received | Not recorded | ➖ |
| Additional diagnostic messages | Partially, in `details` | 🟡 |
| Characteristics identifying the object acted upon | `resource_ids` (`orbId`), `resource_types`, `details.before` | ✅ |
| **Individual identities of group account users** | `acting_client`, returned as `actingClient` — present only when the acting service differs from the subject | ✅ |
| **Full-text of privileged commands** | `details.query` + `details.variables` — the complete mutation | ✅ |

---

## 3. Why this event selection is adequate — the AU-2(d) rationale

AU-2(d) requires a written rationale for why the selected events support after-the-fact
investigation. This section is that rationale.

**Orbital has one write chokepoint and one resource model.** Every change to design intent passes
through a single function before reaching the graph, and every configuration item is addressed by a
globally unique `orbId`. Auditing at that chokepoint is therefore complete for intent changes by
construction — there is no second write path to miss, and no type of object that escapes the model.

**The selected events cover the three questions an investigation asks of a configuration system:**

1. *What changed, and to what?* — `data` events carry the mutation, its variables, and a before-image,
   so a field-level diff is reconstructable from the record alone without a second store.
2. *Who did it, and on whose behalf?* — `actor` is taken from the authenticated identity and is
   server-stamped, never client-supplied. `acting_client` distinguishes a human acting directly from
   a service acting for them.
3. *Who tried and was refused?* — authentication and authorization failures are recorded even though
   nothing changed, because an intrusion signal has value independent of its outcome.

**Why refusals of business logic are excluded.** A write blocked pending approval is a workflow
state, not a security event; the proposal itself is a durable record in the change-request tables,
and the eventual write is audited when it lands. Recording both would double-count a single intent.

**Why reads are excluded.** Orbital holds design intent for infrastructure, not personal or
regulated data. Read-auditing a configuration database at API granularity produces volume that
obscures the mutation record without adding investigative value.

**Known limits of this rationale** — stated because an honest rationale names its own edges:

- Events are recorded per **request**, not per **entity**. A compound mutation touching five servers
  produces one row, so per-entity attribution within such a request is not exact.
- The before-image is **flat**: it covers the edited type's own fields, derived from the deployed
  schema, but never reaches into an owned child. A change to a child is a separate event with its own
  before-image, so a single event's diff is never the whole story for a nested edit. *(Until
  2026-10-06 the selection was a hand-maintained per-type subset and a field outside it silently
  showed no diff. That narrower gap is closed — the selection is now every field the type has.)*
- Field-level attribution is best-effort; node-level attribution is exact.

---

## 4. Answering "show me the audit log for this period"

This is **AU-7** (audit record reduction and report generation), and it is a read path — reduction
never alters original content or ordering.

`GET /api/v1/audit-log` accepts:

| Parameter | Purpose |
|---|---|
| `since` / `until` | RFC3339 time window (`since` exclusive, `until` inclusive) |
| `event_category` | `data`, `management`, or `auth` |
| `operation_name` | Exact operation, e.g. `deleteBackup` |
| `resource_type` | Configuration item type |
| `orbId` | Specific entities; repeatable, OR-ed, capped at 128 |
| `namespace` | Everything within one data center |
| `limit` / `offset` | Paging; `limit` max 500 |

Results are always newest-first. The same data is browsable in the UI under **Audit Log**, and each
configuration item's detail page carries an **Audit Log** tab scoped to that entity and the part of
its subgraph that has nowhere else to appear.

⚠️ **That tab stops at any owned type that has its own page**, which is the most likely thing for a
reader to misjudge. A server's drives and adapters appear on the server's tab, because they have no
page of their own. A data centre's *servers* do **not** appear on the data centre's tab, because each
server has a page where its events already live. An absence on a parent's tab is therefore not
evidence that nothing happened to the child — use `GET /api/v1/audit-log?orbId=<child>`, or the
child's own page, to ask that question. The rule exists because the rollup is bounded: unbounded, a
data centre expanded to 1,159 orbIds against an endpoint that refuses more than 128.

**Status:** ✅ for querying. ➖ **There is no file export** — no CSV, no NDJSON, no report artifact. A
request for "all privileged actions in March" is served today by paging the JSON API. If an adopter
needs a hand-off artifact, that is a gap to close.

**Change-request review is NOT in this log, by decision.** Who proposed, approved, rejected, merged
or closed a change request is recorded in the approval tables and read through
`GET /api/v1/change-requests/:id` — not here. Routing it into the audit log was considered and
rejected in 2026-09-17: a lifecycle row's identifier is a change-request id like `colo-58`, which the
audit log's `namespace` filter (an `orbId`-prefix match) would never return, so it would have bought
a second copy of the record and still not answered the query it was proposed for. The *entity writes*
a merge produces are audited normally. See `docs/reference/CHANGE-CONTROL.md`.

**Who can read it:** any authenticated orbital user, including the `readonly` role. This is a
deliberate position: the audit log records infrastructure facts — hostnames, serial numbers,
addresses, firmware versions — and orbital's schema carries no credential, password, or key fields,
so the payload holds nothing secret. Adopters who consider their topology sensitive should know that
`source_ip_address` appears on every event and that failed-login records contain the attempted email
address.

---

## 5. Integrity — what protects the record

**The audit tables are append-only, enforced by PostgreSQL.** ✅ *(2026-10-07.)*

- A `BEFORE UPDATE OR DELETE` trigger on all three audit tables refuses the statement outright.
  Orbital installs it at startup and it applies to every caller, including orbital itself.
- **A trigger rather than revoked grants**, because orbital owns its schema — it runs the schema
  migration, so it must — and revoking `UPDATE`/`DELETE` does not bind a table's owner. A trigger
  fires regardless of privilege, so it needs no second credential and no deployment change.
- **Orbital verifies the guard at startup and alerts if it is missing or disabled.** `DROP TRIGGER`
  is one statement, leaves the data looking identical, and makes every later edit succeed silently —
  so orbital asks the database what is installed rather than trusting that it installed it. A
  *disabled* trigger is treated as absent: it reads as present to anything checking only for
  existence, and fires never. Unprotected tables are logged at `ERROR` and counted in
  `orbital_audit_tables_unprotected` — alert on `> 0`.

**What this does not stop.** A superuser can set `session_replication_role = replica` and bypass
every trigger, and anyone with filesystem access to the data directory is outside the database's
reach. This is enforcement against the application, a compromised application credential, and
mistakes — not against a determined DBA. Detecting *that* class needs cryptographic integrity with
an external anchor, which is designed and not built (below).

### Two words that get confused — they describe different things

| Term | Subject | Means |
|---|---|---|
| **Read-only** | the **endpoint** | `GET /api/v1/audit-log` offers no way to create, edit or delete an event. There is no write API |
| **Append-only** | the **data** | Orbital adds events and never updates or deletes them. Records accumulate; none are rewritten |
| **Immutable** | *(avoid)* | Implies enforcement orbital does not yet have. Orbital's API docs said this until 2026-10-06 and now say append-only |

A read-only endpoint over append-only data is not a contradiction — orbital writes the events
internally as a side effect of mutations; callers only ever read them. Neither word means *tamper-proof*.

### Designed, not built 📋

A tamper-evidence design exists: each audit row carries the hash of its predecessor, forming a chain
that breaks detectably if any row is altered or removed, with the chain head published periodically
to immutable external storage.

**What that would and would not prove**, stated plainly because the distinction is the whole point:

- ✅ It would detect **retroactive alteration or deletion** of any record published before a
  compromise.
- ❌ It would not **prevent** tampering — only make it visible.
- ❌ It would not detect **removal of the most recent records**, those written since the last
  publication. The exposure window equals the publication interval.
- ❌ It would not prove an event was **never recorded**. Integrity is not completeness.
- ❌ It would not establish **non-repudiation** (AU-10). Proving a record was not altered is not the
  same as proving a named individual performed the act.

---

## 6. Retention

**Orbital prescribes no retention period and prunes nothing.** ✅ by design, ➖ as mechanism —
records accumulate indefinitely and no pruning capability exists yet.

This is deliberate. Orbital cannot know an adopter's obligations, and most frameworks leave the
period to the organization. Reference points, for the adopter's decision — **orbital asserts none of
these**:

| Framework | Retention |
|---|---|
| NIST 800-53 AU-11 | Organization-defined |
| ISO 27001:2022 A.8.15 | Organization-determined; no number given |
| SOC 2 | No numeric retention in the criteria |
| PCI DSS v4.x Req 10.5.1 | 12 months, with 3 immediately available |

When pruning arrives it must preserve the integrity chain, which constrains it to removing whole
published segments rather than arbitrary rows.

---

## 7. Control coverage

| Control | What it asks | Orbital | Status |
|---|---|---|---|
| **AU-2** Event logging | Log the selected event types | Intent mutations, management operations, authentication events, and every authorization refusal | ✅ |
| **AU-2(d)** Rationale | Written justification of the selection | §3 of this document | ✅ |
| **AU-3** Record content | Six required facts per record | §2; outcome is implicit | 🟡 |
| **AU-3(1)** Additional content | FedRAMP's six-item list | Three of six; see §2 | 🟡 |
| **AU-4** Storage capacity | Allocate capacity for retention | No growth monitoring, no capacity signal | ➖ |
| **AU-5** Failure response | Alert on audit logging failure | Failures are still swallowed by design (an audit write must not fail a request) but now increment `orbital_audit_write_failures_total` and log at ERROR, so they are alertable | ✅ |
| **AU-6** Review and analysis | Review logs on a cadence | Orbital provides the surface; the review *practice* is the adopter's | 🟡 |
| **AU-7** Reduction and reporting | Produce filtered reports | Query API and UI; **no file export** | 🟡 |
| **AU-8** Time stamps | UTC, defined granularity | Stored and served UTC at sub-second precision (RFC 3339 with fractional seconds) | ✅ |
| **AU-9(a)** Protect audit info | Protect from unauthorized access, modification, deletion | Append-only **enforced by a PostgreSQL trigger** on all three tables; read access is any authenticated user (§4) | ✅ |
| **AU-9(b)** Alert on detection | Alert when tampering is detected | Startup verification detects a removed or disabled guard; `ERROR` + `orbital_audit_tables_unprotected`. Detects the *protection* being removed, not a superuser bypass | 🟡 |
| **AU-9(2)** Separate system | Store records on a separate system | Not implemented | ➖ |
| **AU-9(3)** Cryptographic protection | Cryptographic integrity protection | Designed, not built — §5 | 📋 |
| **AU-10** Non-repudiation | Irrefutable evidence an individual acted | Not addressed; see §5 | ➖ |
| **AU-11** Retention | Retain for a defined period | Indefinite by default, operator-configurable period not yet implemented | 🟡 |
| **AU-12** Record generation | Generate records at defined components | Single write chokepoint; records generated for all audited events | ✅ |

**AU-9(3) is a High-baseline enhancement.** It is not in the Low or Moderate baselines. An adopter
targeting Moderate should read §5 as a hardening measure rather than a requirement — though AU-9(b)
asks for tamper *detection* at every baseline, and orbital has none today.

---

## 8. Known gaps

Named rather than omitted. Each is tracked work.

1. **Audit-logging failures are silent.** A failed audit write produces a single application-log
   warning, no metric and no alert. AU-5(a) asks for an alert at every baseline. *(Highest-value gap
   here.)*
2. **No tamper detection.** AU-9(b) asks for an alert on detection of unauthorized modification; there
   is nothing to detect with. The §5 design addresses this.
3. **Re-deciding a change request erases the earlier decision.** An approver who rejects and later
   approves leaves only the approval — the rejection and its comment are deleted, because re-deciding
   is implemented as delete-then-create. This is the one genuine hole in the review record.
4. **No file export.** §4.
5. **No capacity signal.** Retention is unbounded by default with no growth metric — AU-4.
9. **No detection of a superuser bypass.** The append-only guard is enforced and monitored (§5), but a
   superuser can disable triggers for their session, and nothing would see it. That gap is what the
   hash chain and external anchor exist to close.

---

## Where to go next

| You want | Read |
|---|---|
| The implementation, conventions and settled decisions | `docs/reference/AUDIT.md` |
| How to call the audit API | `docs/api-cheatsheet.md` |
| Change requests and approval policy | `docs/reference/CHANGE-CONTROL.md` |
| Authenticating to orbital | `docs/auth.md` |
