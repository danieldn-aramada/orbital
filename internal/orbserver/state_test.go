package orbserver

import (
	"sync"
	"testing"

	"github.com/armada/orbital/internal/orb"
)

func TestState_InitialSnapshot(t *testing.T) {
	s := newImportState()
	snap := s.snapshot()
	if snap.Status != "idle" {
		t.Errorf("Status: got %q, want %q", snap.Status, "idle")
	}
	if snap.CurrentVersion != "" {
		t.Errorf("CurrentVersion: got %q, want empty", snap.CurrentVersion)
	}
	if snap.LastError != "" {
		t.Errorf("LastError: got %q, want empty", snap.LastError)
	}
	if snap.LastImport != nil {
		t.Errorf("LastImport: expected nil")
	}
}

func TestState_RunningTransition(t *testing.T) {
	s := newImportState()
	s.setRunning()
	snap := s.snapshot()
	if snap.Status != "running" {
		t.Errorf("Status: got %q, want %q", snap.Status, "running")
	}
	if snap.LastError != "" {
		t.Errorf("LastError should be cleared on setRunning, got %q", snap.LastError)
	}
}

func TestState_DoneTransition(t *testing.T) {
	s := newImportState()
	s.setRunning()
	rec := orb.ImportRecord{Tag: "v3", Status: "done", Verification: orb.VerificationVerified}
	s.setDone(rec)
	snap := s.snapshot()
	if snap.Status != "done" {
		t.Errorf("Status: got %q, want %q", snap.Status, "done")
	}
	if snap.CurrentVersion != "v3" {
		t.Errorf("CurrentVersion: got %q, want %q", snap.CurrentVersion, "v3")
	}
	if snap.AvailableVersion != "" {
		t.Errorf("AvailableVersion: should be cleared after done, got %q", snap.AvailableVersion)
	}
	if snap.LastError != "" {
		t.Errorf("LastError: should be cleared, got %q", snap.LastError)
	}
	if snap.LastImport == nil || snap.LastImport.Tag != "v3" {
		t.Errorf("LastImport not set correctly: %+v", snap.LastImport)
	}
}

func TestState_FailedTransition(t *testing.T) {
	s := newImportState()
	s.setRunning()
	s.setFailed("pull: connection refused")
	snap := s.snapshot()
	if snap.Status != "failed" {
		t.Errorf("Status: got %q, want %q", snap.Status, "failed")
	}
	if snap.LastError != "pull: connection refused" {
		t.Errorf("LastError: got %q, want %q", snap.LastError, "pull: connection refused")
	}
}

func TestState_HydrateFromHistory(t *testing.T) {
	t.Run("empty history leaves state at defaults", func(t *testing.T) {
		s := newImportState()
		s.hydrateFromHistory(nil)
		snap := s.snapshot()
		if snap.LastImport != nil {
			t.Errorf("LastImport: expected nil, got %+v", snap.LastImport)
		}
		if snap.CurrentVersion != "" {
			t.Errorf("CurrentVersion: got %q, want empty", snap.CurrentVersion)
		}
	})

	t.Run("seeds from most recent done record", func(t *testing.T) {
		s := newImportState()
		s.hydrateFromHistory([]orb.ImportRecord{
			{Tag: "v1", Status: "done"},
			{Tag: "v2", Status: "done"},
		})
		snap := s.snapshot()
		if snap.LastImport == nil || snap.LastImport.Tag != "v2" {
			t.Errorf("LastImport: expected v2, got %+v", snap.LastImport)
		}
		if snap.CurrentVersion != "v2" {
			t.Errorf("CurrentVersion: got %q, want %q", snap.CurrentVersion, "v2")
		}
	})

	t.Run("skips failed records, falls back to last done", func(t *testing.T) {
		s := newImportState()
		s.hydrateFromHistory([]orb.ImportRecord{
			{Tag: "v1", Status: "done"},
			{Tag: "v2", Status: "failed"},
		})
		snap := s.snapshot()
		if snap.LastImport == nil || snap.LastImport.Tag != "v1" {
			t.Errorf("LastImport: expected v1, got %+v", snap.LastImport)
		}
		if snap.CurrentVersion != "v1" {
			t.Errorf("CurrentVersion: got %q, want %q", snap.CurrentVersion, "v1")
		}
	})

	t.Run("all failed leaves state at defaults", func(t *testing.T) {
		s := newImportState()
		s.hydrateFromHistory([]orb.ImportRecord{
			{Tag: "v1", Status: "failed"},
		})
		snap := s.snapshot()
		if snap.LastImport != nil {
			t.Errorf("LastImport: expected nil, got %+v", snap.LastImport)
		}
		if snap.CurrentVersion != "" {
			t.Errorf("CurrentVersion: got %q, want empty", snap.CurrentVersion)
		}
	})
}

func TestState_ConcurrentAccess(t *testing.T) {
	s := newImportState()
	var wg sync.WaitGroup
	// 10 readers + 2 writers running concurrently — should not data-race.
	for range 10 {
		wg.Go(func() { _ = s.snapshot() })
	}
	wg.Go(s.setRunning)
	wg.Go(func() {
		s.setDone(orb.ImportRecord{Tag: "v1", Status: "done"})
	})
	wg.Wait()
}
