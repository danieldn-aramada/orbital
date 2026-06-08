package handler

import (
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/google/uuid"
)

// ── isMissedRun ───────────────────────────────────────────────────────────────

func TestIsMissedRun_Disabled(t *testing.T) {
	s := &ent.BackupSchedule{
		Enabled:   false,
		CronSpec:  "* * * * *",
		CreatedAt: time.Now().Add(-2 * time.Minute),
	}
	if isMissedRun(s) {
		t.Error("expected false for disabled schedule")
	}
}

func TestIsMissedRun_InvalidCronSpec(t *testing.T) {
	s := &ent.BackupSchedule{
		Enabled:   true,
		CronSpec:  "not a cron expression",
		CreatedAt: time.Now().Add(-2 * time.Minute),
	}
	if isMissedRun(s) {
		t.Error("expected false for invalid cron spec")
	}
}

func TestIsMissedRun_NotYetDue(t *testing.T) {
	// Just triggered — Next(now) always returns a future tick regardless of wall clock position.
	now := time.Now()
	s := &ent.BackupSchedule{
		Enabled:         true,
		CronSpec:        "0 * * * *", // top of every hour
		CreatedAt:       now.Add(-2 * time.Hour),
		LastTriggeredAt: &now,
	}
	if isMissedRun(s) {
		t.Error("expected false: just triggered, next run has not arrived yet")
	}
}

func TestIsMissedRun_Overdue(t *testing.T) {
	// Every-minute schedule; last triggered 5 minutes ago → missed runs.
	old := time.Now().Add(-5 * time.Minute)
	s := &ent.BackupSchedule{
		Enabled:         true,
		CronSpec:        "* * * * *",
		CreatedAt:       time.Now().Add(-10 * time.Minute),
		LastTriggeredAt: &old,
	}
	if !isMissedRun(s) {
		t.Error("expected true: schedule is overdue")
	}
}

func TestIsMissedRun_FallsBackToCreatedAt(t *testing.T) {
	// No LastTriggeredAt; created 5 minutes ago with every-minute spec.
	s := &ent.BackupSchedule{
		Enabled:         true,
		CronSpec:        "* * * * *",
		CreatedAt:       time.Now().Add(-5 * time.Minute),
		LastTriggeredAt: nil,
	}
	if !isMissedRun(s) {
		t.Error("expected true when using CreatedAt fallback and interval elapsed")
	}
}

// ── buildCronSpec ─────────────────────────────────────────────────────────────

func TestBuildCronSpec_ReturnsSpecAndLocation(t *testing.T) {
	s := &ent.BackupSchedule{
		CronSpec: "0 0 * * *",
		Timezone: "America/Los_Angeles",
	}
	spec, loc := buildCronSpec(s)
	if spec != "0 0 * * *" {
		t.Errorf("expected spec %q, got %q", "0 0 * * *", spec)
	}
	if loc.String() != "America/Los_Angeles" {
		t.Errorf("expected America/Los_Angeles, got %s", loc.String())
	}
}

func TestBuildCronSpec_InvalidTimezone_FallsBackToUTC(t *testing.T) {
	s := &ent.BackupSchedule{
		CronSpec: "0 0 * * *",
		Timezone: "Not/AReal/Zone",
	}
	_, loc := buildCronSpec(s)
	if loc != time.UTC {
		t.Error("expected fallback to UTC for invalid timezone")
	}
}

// ── toBackupResponse ──────────────────────────────────────────────────────────

func TestToBackupResponse_IncludesTrigger(t *testing.T) {
	j := &ent.Backup{
		ID:        uuid.New(),
		Status:    "completed",
		Trigger:   backup.TriggerScheduled,
		CreatedAt: time.Now(),
	}
	r := toBackupResponse(j)
	if r.Trigger != "scheduled" {
		t.Errorf("expected trigger=scheduled, got %q", r.Trigger)
	}
}

func TestToBackupResponse_ManualTrigger(t *testing.T) {
	j := &ent.Backup{
		ID:        uuid.New(),
		Status:    "pending",
		Trigger:   backup.TriggerManual,
		CreatedAt: time.Now(),
	}
	r := toBackupResponse(j)
	if r.Trigger != "manual" {
		t.Errorf("expected trigger=manual, got %q", r.Trigger)
	}
}

// ── formatDuration ────────────────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{24 * time.Hour, "1d"},
		{7 * 24 * time.Hour, "7d"},
		{time.Hour, "1h"},
		{12 * time.Hour, "12h"},
		{30 * time.Minute, "30m"},
		{0, "0s"},
		{90 * time.Second, "1m30s"},
	}
	for _, c := range cases {
		got := formatDuration(c.d)
		if got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
