# 005 — PostgreSQL-Backed Backup Scheduler

**Status:** Planned — not yet implemented

**Date:** 2026-06-05

---

## Decision

Use PostgreSQL as the source of truth for backup scheduling. An in-process Go ticker reads the schedule from a `backup_schedules` table on each tick, computes whether a run is due, and triggers one if so. `pg_try_advisory_xact_lock` prevents concurrent triggers in future multi-replica deployments.

**Interval-based, not cron-based.** Store a duration (`interval_seconds`) and optional `run_at_time` (HH:MM, UTC or named timezone) rather than a cron expression. Go's stdlib `time` package handles "next fire time" arithmetic without any additional dependencies. Cron expression syntax (`robfig/cron`) is not needed.

---

## Why PostgreSQL state (not env var only)

An env var gives the interval but nothing else. On restart, the process must compare against `backup_records.completed_at` to decide whether a run was missed — and if the interval changed between restarts, the reasoning is ambiguous. PostgreSQL-owned state keeps schedule config and execution history in the same place. Admin UI can modify the schedule without a restart.

---

## Scheduling model: interval + optional time-of-day

Two fields on `backup_schedules`:

| Field | Type | Example | Semantics |
|---|---|---|---|
| `interval_seconds` | int | `86400` | Run every N seconds since last trigger |
| `run_at_hour` | int, nullable | `2` | If set, only trigger when local hour matches (UTC) |
| `run_at_minute` | int, nullable | `0` | Combined with run_at_hour for "2:00 AM UTC" |

**Pure stdlib "midnight" computation:**
```go
// "Next midnight UTC after lastTriggeredAt"
last := schedule.LastTriggeredAt
nextMidnight := time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, time.UTC)
if time.Now().UTC().After(nextMidnight) { // trigger }
```

No external package needed. `time.Date` handles month/year rollover automatically.

**If time-of-day is not set:** fire whenever `now - lastTriggeredAt >= interval`. Simple duration check.

**If time-of-day is set:** fire when `now - lastTriggeredAt >= interval` AND `now.Hour() == runAtHour && now.Minute() < runAtMinute+5` (5-minute window tolerates tick jitter).

**Bootstrap env var:** `ORBITAL_BACKUP_INTERVAL=24h` seeds the initial row if none exists. After that, PostgreSQL owns the schedule.

---

## Advisory lock

`pg_try_advisory_xact_lock(5555100001)` wraps the check-and-trigger step in the ticker. Transaction-scoped: auto-releases if process crashes (no orphaned locks). Returns false immediately if another instance holds it — that instance handles the tick.

Single replica today: lock is never contested, overhead is one SQL row.
Multi-replica future: first instance to tick wins; others skip.

---

## Implementation Plan

### Step 1 — `backup_schedules` ent schema
**New file:** `ent/schema/backup_schedule.go`

Fields:
- `interval_seconds` — `field.Int("interval_seconds")` (required)
- `run_at_hour` — `field.Int("run_at_hour").Optional().Nillable()` (0–23, nil = any hour)
- `run_at_minute` — `field.Int("run_at_minute").Optional().Nillable()` (0–59, nil = any minute)
- `enabled` — `field.Bool("enabled").Default(true)`
- `last_triggered_at` — `field.Time("last_triggered_at").Optional().Nillable()`

Use `AuditMixin` (created_at, updated_at, created_by, updated_by).

Run `go generate ./ent/...` after.

Add `"backup_schedules"` to `truncateAll` in `internal/testutil/db.go`.

---

### Step 2 — Config
**File:** `internal/config/config.go`

`BackupSchedule string` already exists (`ORBITAL_BACKUP_INTERVAL`, default `""`). Parse it as `time.Duration` in `BackupConfig`. If set and no DB row exists → bootstrap row on startup.

No other config changes needed.

---

### Step 3 — `BackupConfig` / `BackupHandler` additions
**File:** `internal/handler/backup.go`

Add to `BackupConfig`:
```go
BootstrapInterval string  // value of ORBITAL_BACKUP_INTERVAL; empty = no bootstrap
```

Add to `BackupHandler`:
```go
bootstrapInterval string
```

---

### Step 4 — Advisory lock helper
**File:** `internal/handler/backup.go`

```go
const schedulerAdvisoryLockKey int64 = 5555100001

// tryAdvisoryLock attempts a PostgreSQL transaction-scoped advisory lock.
// Returns (acquired=true, unlock func) or (acquired=false, nil).
// The unlock func rolls back the transaction, which releases the lock.
func tryAdvisoryLock(ctx context.Context, db *ent.Client) (bool, func(), error)
```

Implementation:
1. `db.Driver().(*entsql.Driver).DB()` → raw `*sql.DB`
2. `rawDB.BeginTx(ctx, nil)` → transaction
3. `SELECT pg_try_advisory_xact_lock($1)` → scan bool
4. If false: `tx.Rollback()`, return `(false, nil, nil)`
5. If true: return `(true, func() { tx.Rollback() }, nil)`

Import: `entsql "entgo.io/ent/dialect/sql"` (already in go.mod, just needs the alias).

---

### Step 5 — `BootstrapSchedule` method
**File:** `internal/handler/backup.go`

```go
func (h *BackupHandler) BootstrapSchedule(ctx context.Context) error
```

1. Query for existing row → if found, return nil (DB owns it)
2. If `h.bootstrapInterval == ""`, return nil
3. Parse `time.ParseDuration(h.bootstrapInterval)` → error if invalid
4. Create row: `interval_seconds = int(d.Seconds())`, enabled=true, created_by="system"
5. Log: `h.logger.Info("bootstrapped backup schedule", "interval", h.bootstrapInterval)`

---

### Step 6 — `isRunDue` helper
**File:** `internal/handler/backup.go`

```go
func isRunDue(schedule *ent.BackupSchedule) bool
```

Logic:
1. If not enabled: false
2. `ref := schedule.CreatedAt` (fallback for first run)
   If `schedule.LastTriggeredAt != nil`: `ref = *schedule.LastTriggeredAt`
3. `elapsed := time.Since(ref.UTC())`
4. `interval := time.Duration(schedule.IntervalSeconds) * time.Second`
5. If `elapsed < interval`: return false
6. If `schedule.RunAtHour == nil`: return true (any time is fine)
7. Now check time-of-day window: `now := time.Now().UTC()`
   Return `now.Hour() == *schedule.RunAtHour && now.Minute() >= *schedule.RunAtMinute && now.Minute() < *schedule.RunAtMinute+5`

The 5-minute window ensures the 60-second ticker doesn't miss the target minute.

---

### Step 7 — `checkAndTrigger` + `StartScheduler`
**File:** `internal/handler/backup.go`

```go
func (h *BackupHandler) checkAndTrigger(ctx context.Context)
```

1. `tryAdvisoryLock` → skip if not acquired, `defer unlock()`
2. Query schedule → return if not found
3. `isRunDue(schedule)` → return if false
4. Query for pending/running backup → if exists, log and return (don't update last_triggered_at; retry next tick)
5. Create backup record: `trigger = "scheduled"`, `created_by = "scheduler"`
6. Update `last_triggered_at = time.Now().UTC()` on schedule row (BEFORE launching goroutine)
7. `go h.runBackup(job.ID)`
8. Write audit event: operation `"createBackup"`, details `{jobId, trigger: "scheduled"}`
9. Log: `h.logger.Info("scheduled backup triggered", "jobId", job.ID)`

```go
func (h *BackupHandler) StartScheduler(ctx context.Context)
```

1. `h.BootstrapSchedule(ctx)` — log error but continue
2. Run `h.checkAndTrigger(ctx)` once immediately (catch-up on restart)
3. `ticker := time.NewTicker(60 * time.Second)`
4. Loop: `select { case <-ticker.C: h.checkAndTrigger(ctx) case <-ctx.Done(): ticker.Stop(); return }`

---

### Step 8 — `Trigger` handler: accept `trigger` field
Parse optional JSON body `{"trigger": "manual"|"scheduled"}`, default `"manual"`. Validate. `.SetTrigger(backup.Trigger(body.Trigger))`.

---

### Step 9 — `backupResponse` includes `trigger`
Add `Trigger string \`json:"trigger"\`` to `backupResponse`. Set in `toBackupResponse`.

---

### Step 10 — Schedule REST endpoints
Add to `BackupHandler`:

**`GET /api/v1/backup/schedule`** → `GetSchedule`:
- Returns null (JSON) if no row
- Otherwise: `{intervalSeconds, runAtHour, runAtMinute, enabled, lastTriggeredAt, nextRunApprox, createdAt}`
- `nextRunApprox`: `ref.Add(interval)` — simple approximation, not guaranteed exact when time-of-day is set

**`PUT /api/v1/backup/schedule`** → `UpdateSchedule`:
- Body: `{intervalSeconds int, runAtHour *int, runAtMinute *int, enabled *bool}`
- Validate: `intervalSeconds > 0` (required on create, optional on update)
- Upsert: create if none exists, update if exists
- Set `updated_by = actorFromContext(c)`
- Write audit event: `"updateBackupSchedule"`
- Return updated schedule in same shape as GET

---

### Step 11 — Wire into `server.go`
- `api.GET("/backup/schedule", bk.GetSchedule)`
- `api.PUT("/backup/schedule", bk.UpdateSchedule)`
- Add `backupHandler *handler.BackupHandler` field to `Server` struct
- Set in `New()` after `bk` is created
- In `Start()`: `if s.backupHandler != nil { go s.backupHandler.StartScheduler(ctx) }`

The `ctx` in `Start()` is signal-aware; cancellation shuts down the scheduler cleanly.

---

### Step 12 — `page.Backups` data struct
**File:** `internal/web/data/page/backups.go`

Add: `HasSchedule bool`, `ScheduleEnabled bool`, `IntervalSeconds int`, `RunAtHour *int`, `RunAtMinute *int`, `NextRunApprox string`, `LastTriggeredAt string`.

---

### Step 13 — `UI.Backups` handler populates schedule data
**File:** `internal/handler/ui.go`

Query `backup_schedules` on page render. Compute `nextRunApprox`. Pass into page struct.

---

### Step 14 — Backups template
**File:** `web/orbital/templates/pages/backups.gohtml`

- Add "Trigger" column header (colspan 5 → 6 in loading/empty rows)
- Add schedule section: `<div id="schedule-section" data-can-mutate="{{.CanMutate}}">`

---

### Step 15 — JavaScript
**File:** `web/shared/static/app.js`

- `loadSchedule()`: fetch `/api/v1/backup/schedule`, render interval, enabled status, next run, last triggered, toggle button (if canMutate)
- `toggleSchedule(enabled)`: PUT `{enabled}`, reload
- Update `renderBackups` to add Trigger column
- Call `loadSchedule()` on `DOMContentLoaded` when schedule section exists

---

## Test Plan

### Unit tests (no services)

| Test | What |
|---|---|
| `TestIsRunDue_NotEnough Elapsed` | interval not elapsed → false |
| `TestIsRunDue_Elapsed_NoTimeOfDay` | interval elapsed, no hour set → true |
| `TestIsRunDue_Elapsed_WrongHour` | interval elapsed but wrong hour → false |
| `TestIsRunDue_Elapsed_CorrectHour` | interval elapsed, hour+minute match window → true |
| `TestIsRunDue_Disabled` | enabled=false → always false |
| `TestIsRunDue_FirstRun_FallsBackToCreatedAt` | last_triggered_at is nil, uses createdAt |
| `TestUpdateSchedule_ZeroInterval_Returns400` | intervalSeconds=0 on create |
| `TestGetSchedule_NilDB_Returns200Null` | handler with nil DB returns null without panic |
| `TestBackupResponse_IncludesTrigger` | toBackupResponse includes trigger field |

### Integration tests (requires `make up`)

| Test | What |
|---|---|
| `TestBootstrapSchedule_CreatesRow` | env var set, no row → row created |
| `TestBootstrapSchedule_IgnoresEnvWhenRowExists` | row exists → env var ignored |
| `TestBootstrapSchedule_NoopWhenEmpty` | empty env var → no row |
| `TestGetSchedule_NoSchedule` | 200 + null body |
| `TestGetSchedule_WithSchedule` | correct shape, nextRunApprox set |
| `TestUpdateSchedule_Create` | creates new row |
| `TestUpdateSchedule_Update` | updates cron expression |
| `TestUpdateSchedule_ToggleEnabled` | enabled ↔ disabled round-trip |
| `TestAdvisoryLock_SecondCallReturnsFalse` | concurrent calls: first gets lock, second returns false |
| `TestAdvisoryLock_ReleasedAfterUnlock` | after unlock(), next call acquires |
| `TestScheduledBackup_TriggerField` | backup created by scheduler has trigger=scheduled |
| `TestBackupList_IncludesTriggerField` | list endpoint includes trigger for all records |

---

## Settled decisions

1. **Interval-based scheduling, not cron expressions** — `interval_seconds` + optional `run_at_hour`/`run_at_minute`. Go stdlib handles time arithmetic. No `robfig/cron` dependency.
2. **`ORBITAL_BACKUP_INTERVAL` bootstraps initial row** — env var seeds first schedule row; PostgreSQL owns it after that. Env var ignored if a row already exists.
3. **`pg_try_advisory_xact_lock(5555100001)` guards the scheduler tick** — transaction-scoped (auto-releases on crash), non-blocking (`try` variant), allows future multi-replica deployment without code changes.
4. **`last_triggered_at` updated before `go runBackup`** — crash-safe: pending record blocks re-trigger on restart.
5. **Catch-up fires at most once on startup** — `checkAndTrigger` runs once immediately on `StartScheduler`. One backup regardless of how many intervals were missed.
6. **60-second ticker loop** — simple, testable, no cron scheduler goroutine pool needed.
7. **Scheduler lives in `BackupHandler.StartScheduler`** — started from `Server.Start()` using the signal-aware context from `main()`.
