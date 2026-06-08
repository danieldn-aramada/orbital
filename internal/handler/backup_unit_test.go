package handler

import (
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/google/uuid"
)

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
