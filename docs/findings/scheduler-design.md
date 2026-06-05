# Scheduler Design: How In-Process Job Scheduling Works

> Research conducted June 2026 while designing the Orbital backup scheduler.
> Topic: is polling standard? what do production Go services do? how does sleep-until-next-run work?

---

## The Problem

We needed to run a DGraph backup daily at midnight PT, with the schedule stored in PostgreSQL so it can be changed without redeploying. The scheduler must work locally (docker-compose) and in AKS — ruling out K8s CronJob as the sole mechanism.

---

## Option 1: Polling Loop (What We Built First)

```
loop every 60 seconds:
  read schedule from DB
  if interval elapsed AND current time in [target_hour:00, target_hour:05):
    run backup
```

**How it works:** A goroutine wakes up every 60 seconds, reads the DB, and checks two conditions.

**The bug:** Both conditions must pass simultaneously. If the process restarts at 08:06 UTC (one minute past the 5-minute window), Gate 1 passes but Gate 2 fails. Every subsequent tick that day also fails. **That day's backup is silently skipped.**

This is a real production hazard: deployments, node drains, OOM kills, and GC pauses all happen during the target window regularly enough.

---

## Option 2: sleep-until-next-run (What We Switched To)

This is the standard approach used by every serious scheduling library.

**Core idea:** instead of waking up every 60 seconds to ask "is it time yet?", compute exactly when the next run should happen and sleep until that moment.

```
loop:
  next = compute_next_run_time(schedule)   // e.g. "tomorrow at 00:00 PT"
  sleep until next                          // time.NewTimer(time.Until(next))
  run_backup()
```

**Go primitive:** `time.Timer`, not `time.Ticker`.
- `time.Ticker` fires on a fixed interval (every N seconds) — for polling.
- `time.NewTimer(d)` fires once after duration `d` — for "wake me at this specific moment."

**Why this is correct for calendar scheduling:** you compute the next occurrence of the schedule, create a timer that fires at exactly that moment, and your goroutine is parked (zero CPU) until then. A daily backup means the scheduler goroutine sleeps for ~24 hours between firings.

**DST-safety:** Use `time.Date(year, month, day, hour, minute, 0, 0, loc)` with a `*time.Location` from `time.LoadLocation("America/Los_Angeles")`. Go's time package resolves wall-clock times through the IANA timezone database, so midnight PT on the day after a DST transition is computed correctly. Never use `now.Add(24 * time.Hour)` for calendar scheduling — this drifts by one hour across DST boundaries.

---

## What robfig/cron Does

`robfig/cron` v3 uses a **priority queue + single timer** model:

1. All registered jobs are sorted by their next scheduled run time.
2. A single `time.Timer` is set to fire at the earliest next-run time.
3. When the timer fires: run all due jobs, compute each job's next run, re-sort, set new timer.
4. Between firings: goroutine is parked on `select` — zero CPU, zero polls.

It supports standard cron expressions (`0 0 * * *` = daily at midnight) and timezone-aware scheduling via `cron.WithLocation(loc)` or inline `CRON_TZ=America/Los_Angeles` in the expression.

**Why we use it:** DST handling. A naive "add 24h" or "check if current hour matches" approach gets the DST transition days wrong. `robfig/cron` evaluates the cron expression against wall-clock time in the given timezone, so `0 0 * * *` in `America/Los_Angeles` always means midnight local time — even on the two DST transition days when the day is 23 or 25 hours long.

---

## What pg_cron Does (Why We Don't Use It)

`pg_cron` is a PostgreSQL extension that runs cron jobs inside the database process. It uses a background worker that sleeps until the next scheduled time — same sleep-until-next-run pattern. The difference: the "job" is a SQL statement.

Not applicable here: our backup job calls the DGraph admin API, uploads to S3, and records results in PostgreSQL. The logic lives in Go. pg_cron would just be a trigger that fires an HTTP call — more complexity, no benefit.

---

## What We Built (Final Design)

```
robfig/cron timer  ──fires at midnight PT──▶  fire()
                                                │
                                                ├─ pg_try_advisory_xact_lock   (leader election)
                                                ├─ check enabled flag in DB    (dynamic on/off)
                                                ├─ check no backup in progress
                                                ├─ create backup job row
                                                ├─ update last_triggered_at    (crash-safe dedup)
                                                └─ go runBackup()
```

**Schedule storage (PostgreSQL `backup_schedules`):**
- `interval_seconds` — e.g. 86400 for daily
- `run_at_hour` / `run_at_minute` — local time (0–23, 0–59)
- `timezone` — IANA name, e.g. `"America/Los_Angeles"`; defaults to `"UTC"`
- `enabled` — on/off toggle without deleting the row
- `last_triggered_at` — updated before launching goroutine (crash-safe: prevents re-trigger on restart)

**How the cron spec is built at runtime:**
- With `run_at_hour` set: `"minute hour * * *"` cron expression + location from timezone
- Without: `"@every Nh"` / `"@every Nd"` (pure interval, location ignored)

**Startup catch-up:** on server start, before the cron begins, we check whether a run was missed during downtime using calendar-day logic (not a window check):
- For time-of-day schedules: if today's target time has passed and `last_triggered_at` < today's target → fire immediately
- For interval schedules: if `time.Since(last_triggered_at) >= interval` → fire immediately

**Cron restart on schedule change:** `PUT /api/v1/backup/schedule` updates the DB row then calls `restartCron()` — stops the old `*cron.Cron`, reads the new schedule, starts a fresh one. Uses a mutex so restarts are safe from concurrent requests.

**Multi-replica safety:** `pg_try_advisory_xact_lock` is called inside `fire()`. Transaction-scoped: auto-releases on commit/rollback/crash. If two replicas race, only one gets the lock and fires; the other returns immediately.

---

## Why Polling is Still in Kubernetes

K8s kube-controller-manager runs its CronJob controller on a 10-second polling loop — checking all CronJob resources for due schedules. This is polling at the infrastructure level, not the application level. The application (your pod) never polls; it just starts when the Job resource is created. For our use case (in-process scheduler, no dependency on K8s), sleep-until-next-run is correct.

---

## Key Takeaways

| Approach | Precision | CPU between runs | DST-safe | Correctness for "daily at midnight" |
|---|---|---|---|---|
| 60s ticker + window check | ±60s + window miss risk | 1,440 wakeups/day | No | **Bug: misses day if process restarts outside window** |
| sleep-until-next-run (manual) | Milliseconds | ~0 | With `time.Date` + location | Correct |
| `robfig/cron` | Milliseconds | ~0 | Yes (IANA tz) | Correct |

For a daily job, the practical CPU difference is negligible. The correctness difference is significant: the polling approach has a real failure mode that will eventually bite you in production.
