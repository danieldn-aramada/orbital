//go:build integration

package handler

// Tests transcribed from the acceptance list in the orbital-HA design, one
// item to one test name, BEFORE any body was written. The mapping is stated
// on each test so a reader can check coverage against the list rather than
// against the implementation.
//
// Acceptance items 8 (rolling update), 9 (node drain) and 10 (migrations run
// once per deploy) are cluster-level and are NOT covered here — they belong to
// Phase 2. Item 11 is a documentation change. Item 1 (scheduled backup fires
// once) is covered at the mechanism it depends on, the advisory lock, because
// driving fire() end to end would require real S3.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/internal/testutil"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func newPendingExportJob(t *testing.T, db *ent.Client) *ent.ExportJob {
	t.Helper()
	job, err := db.ExportJob.Create().
		SetDatacenterID("colo:dc-test").
		SetDatacenterName("dc-test").
		SetStatus(exportjob.StatusPending).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create export job: %v", err)
	}
	return job
}

// Acceptance 2 (claim half): two concurrent triggers must result in exactly one
// job running. The admission lock stops two ROWS being created; this pins the
// second line of defence — even if two runners reach the same row, only one
// executes it.
func TestJobLease_ClaimIsWonByExactlyOneRunner(t *testing.T) {
	db := testutil.NewTestDB(t)
	job := newPendingExportJob(t, db)

	const runners = 8
	var wg sync.WaitGroup
	results := make([]bool, runners)
	start := make(chan struct{})
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			won, err := claimExportJob(context.Background(), db, job.ID, "runner-"+uuid.NewString())
			if err != nil {
				t.Errorf("runner %d: claim: %v", i, err)
				return
			}
			results[i] = won
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for _, r := range results {
		if r {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("expected exactly 1 runner to win the claim, got %d", won)
	}

	got := db.ExportJob.GetX(context.Background(), job.ID)
	if got.Status != exportjob.StatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.LockedBy == nil || *got.LockedBy == "" {
		t.Error("locked_by not set by the winning claim")
	}
	if got.HeartbeatAt == nil {
		t.Error("heartbeat_at not set by the winning claim")
	}
}

// The fence. A runner whose job was reaped and re-claimed must discover that
// at its next heartbeat and stop, rather than keep a superseded job looking
// healthy. Without this the reaper would resurrect jobs into double execution.
func TestJobLease_HeartbeatFailsOnceAnotherRunnerOwnsTheJob(t *testing.T) {
	db := testutil.NewTestDB(t)
	job := newPendingExportJob(t, db)
	ctx := context.Background()

	won, err := claimExportJob(ctx, db, job.ID, "runner-A")
	if err != nil || !won {
		t.Fatalf("runner-A claim: won=%v err=%v", won, err)
	}
	held, err := beatExportJob(ctx, db, job.ID, "runner-A")
	if err != nil || !held {
		t.Fatalf("runner-A should still hold its own lease: held=%v err=%v", held, err)
	}

	// Simulate the reaper failing the job and runner-B picking it up.
	if _, err := db.ExportJob.UpdateOneID(job.ID).
		SetStatus(exportjob.StatusPending).
		SetLockedBy("runner-B").
		Save(ctx); err != nil {
		t.Fatalf("hand job to runner-B: %v", err)
	}

	held, err = beatExportJob(ctx, db, job.ID, "runner-A")
	if err != nil {
		t.Fatalf("runner-A heartbeat: %v", err)
	}
	if held {
		t.Fatal("runner-A still believes it holds the lease after runner-B took it — the fence is not fencing")
	}
}

// Acceptance 3: a pod killed mid-export leaves a job whose heartbeat stops
// advancing; the reaper must fail it on staleness.
func TestReaper_FailsJobWhoseHeartbeatWentStale(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	job := newPendingExportJob(t, db)

	if _, err := db.ExportJob.UpdateOneID(job.ID).
		SetStatus(exportjob.StatusRunning).
		SetLockedBy("pod-that-died_abc").
		SetHeartbeatAt(time.Now().Add(-5 * time.Minute)).
		Save(ctx); err != nil {
		t.Fatalf("stage stale job: %v", err)
	}

	ReapStaleJobs(ctx, db, nil, JobLeaseConfig{
		HeartbeatInterval: time.Second,
		StaleAfter:        30 * time.Second,
		OrphanGrace:       time.Hour,
	}, slog.Default())

	got := db.ExportJob.GetX(ctx, job.ID)
	if got.Status != exportjob.StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
}

// Acceptance 4 and the negative half of acceptance 7. A reaper that fires when
// nothing died is worse than no reaper: it kills live work, and it trains
// whoever reads the error field to ignore it. This is also what makes starting
// a second replica safe — the new pod's reaper must leave the first pod's
// running job alone.
func TestReaper_DoesNotFailJobWithFreshHeartbeat(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	job := newPendingExportJob(t, db)

	if _, err := db.ExportJob.UpdateOneID(job.ID).
		SetStatus(exportjob.StatusRunning).
		SetLockedBy("pod-alive_abc").
		SetHeartbeatAt(time.Now()).
		Save(ctx); err != nil {
		t.Fatalf("stage live job: %v", err)
	}

	ReapStaleJobs(ctx, db, nil, JobLeaseConfig{
		HeartbeatInterval: time.Second,
		StaleAfter:        30 * time.Second,
		OrphanGrace:       time.Hour,
	}, slog.Default())

	got := db.ExportJob.GetX(ctx, job.ID)
	if got.Status != exportjob.StatusRunning {
		t.Fatalf("status = %q, want running — the reaper killed a live job", got.Status)
	}
	if got.Error != nil {
		t.Fatalf("error = %q, want none", *got.Error)
	}
}

// Acceptance 7: the marking must name the runner it reaped. An operator
// reading a failed job needs to know which pod to investigate; that is the
// reason locked_by holds a readable locator rather than a bare token.
func TestReaper_ReasonNamesTheRunnerItReaped(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	job := newPendingExportJob(t, db)

	const runner = "orbital-7d9f8c6b5-x2kqp_5f1c2e2a"
	if _, err := db.ExportJob.UpdateOneID(job.ID).
		SetStatus(exportjob.StatusRunning).
		SetLockedBy(runner).
		SetHeartbeatAt(time.Now().Add(-5 * time.Minute)).
		Save(ctx); err != nil {
		t.Fatalf("stage stale job: %v", err)
	}

	ReapStaleJobs(ctx, db, nil, JobLeaseConfig{StaleAfter: 30 * time.Second}, slog.Default())

	got := db.ExportJob.GetX(ctx, job.ID)
	if got.Error == nil {
		t.Fatal("reaped job has no error message")
	}
	if !strings.Contains(*got.Error, runner) {
		t.Fatalf("error %q does not name the runner %q", *got.Error, runner)
	}
}

// The upgrade rule. Jobs left running by the deploy that introduced leases have
// no heartbeat at all, and under a ROLLING update one of them may still be
// executing in the outgoing pod. They must survive the short staleness
// threshold and only be reaped after the much longer orphan grace.
func TestReaper_JobWithNoHeartbeatSurvivesUntilTheOrphanGrace(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	// created_at is immutable and defaults to now, so "recent" is the default.
	recent := newPendingExportJob(t, db)
	if _, err := db.ExportJob.UpdateOneID(recent.ID).SetStatus(exportjob.StatusRunning).Save(ctx); err != nil {
		t.Fatalf("stage recent orphan: %v", err)
	}

	cfg := JobLeaseConfig{StaleAfter: time.Second, OrphanGrace: time.Hour}
	ReapStaleJobs(ctx, db, nil, cfg, slog.Default())

	got := db.ExportJob.GetX(ctx, recent.ID)
	if got.Status != exportjob.StatusRunning {
		t.Fatalf("status = %q, want running — a heartbeat-less job was reaped on the SHORT threshold, "+
			"which would kill work still running in an outgoing pod during a rolling update", got.Status)
	}

	// Past the grace, the same job is reaped.
	ReapStaleJobs(ctx, db, nil, JobLeaseConfig{StaleAfter: time.Second, OrphanGrace: time.Nanosecond}, slog.Default())
	got = db.ExportJob.GetX(ctx, recent.ID)
	if got.Status != exportjob.StatusFailed {
		t.Fatalf("status = %q, want failed once past the orphan grace", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "orphaned") {
		t.Fatalf("orphan reaping should say so, got %v", got.Error)
	}
}

// Acceptance 6: persistence is verified through the API a consumer would
// actually call, not by reading back the write path. Asserts both directions —
// present once claimed, absent while pending.
func TestExportJobStatus_ExposesLockedByAndHeartbeat(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	job := newPendingExportJob(t, db)
	h := &Export{db: db, logger: slog.Default()}

	get := func() statusResponse {
		t.Helper()
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/export/jobs/"+job.ID.String(), nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("jobId")
		c.SetParamValues(job.ID.String())
		if err := h.Status(c); err != nil {
			t.Fatalf("Status: %v", err)
		}
		var resp statusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return resp
	}

	// Negative: nothing has claimed it, so the fields must be absent rather
	// than empty strings — a populated lockedBy on an unclaimed job would read
	// as a recorded fact.
	if before := get(); before.LockedBy != nil || before.HeartbeatAt != nil {
		t.Fatalf("pending job exposes lease fields: lockedBy=%v heartbeatAt=%v", before.LockedBy, before.HeartbeatAt)
	}

	const runner = "orbital-abc_def"
	won, err := claimExportJob(ctx, db, job.ID, runner)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}

	after := get()
	if after.LockedBy == nil || *after.LockedBy != runner {
		t.Fatalf("lockedBy = %v, want %q", after.LockedBy, runner)
	}
	if after.HeartbeatAt == nil {
		t.Fatal("heartbeatAt absent after claim")
	}
	if _, err := time.Parse(time.RFC3339, *after.HeartbeatAt); err != nil {
		t.Fatalf("heartbeatAt %q is not RFC3339: %v", *after.HeartbeatAt, err)
	}
}

// Acceptance 1, at the mechanism it rests on. The scheduled backup fires once
// per tick because fire() holds this lock; driving fire() itself would need
// real object storage. Guards the lock against a regression that would let
// every replica fire its own backup.
func TestAdvisoryLock_IsHeldByOnlyOneCallerAtATime(t *testing.T) {
	db := testutil.NewTestDB(t)
	rawDB := testutil.RawTestDB(t)
	_ = db

	ctx := context.Background()
	first, release, err := tryAdvisoryLockKey(ctx, rawDB, reaperAdvisoryLockKey)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !first {
		t.Fatal("first caller did not acquire a free lock")
	}

	second, _, err := tryAdvisoryLockKey(ctx, rawDB, reaperAdvisoryLockKey)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second {
		t.Fatal("second caller acquired a lock already held — concurrent replicas would both act")
	}

	// A different concern uses a different key and must not be blocked.
	other, releaseOther, err := tryAdvisoryLockKey(ctx, rawDB, jobAdmissionAdvisoryLockKey)
	if err != nil {
		t.Fatalf("other-key acquire: %v", err)
	}
	if !other {
		t.Fatal("admission lock blocked by the reaper lock — keys are not independent")
	}
	releaseOther()

	release()
	again, releaseAgain, err := tryAdvisoryLockKey(ctx, rawDB, reaperAdvisoryLockKey)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if !again {
		t.Fatal("lock not released")
	}
	releaseAgain()
}
