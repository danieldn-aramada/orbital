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
