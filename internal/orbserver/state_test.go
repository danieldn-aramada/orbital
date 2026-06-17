package orbserver

import (
	"errors"
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

// TestState_NotePollErrorPreservesAvailable pins the load-bearing rule for the
// poller's error path: a transient registry error MUST NOT erase a previously-
// discovered availableVersion. The banner "New version available: v4" the
// operator just published would otherwise vanish on the first network blip.
func TestState_NotePollErrorPreservesAvailable(t *testing.T) {
	s := newImportState()
	s.setAvailable("v4")

	s.notePollError(errors.New("dial tcp: i/o timeout"))

	snap := s.snapshot()
	if snap.AvailableVersion != "v4" {
		t.Errorf("AvailableVersion must survive transient poll error; got %q", snap.AvailableVersion)
	}
	if snap.LastPollErr != "dial tcp: i/o timeout" {
		t.Errorf("LastPollErr: got %q, want the dial error message", snap.LastPollErr)
	}
	if snap.LastChecked.IsZero() {
		t.Error("LastChecked must update on error path so the UI can show 'last tried at ...'")
	}
}

// TestState_SetAvailableClearsPollErr pins the recovery rule: a successful poll
// after a string of failed ones must clear LastPollErr so the UI stops showing
// a stale "polling failed" indicator.
func TestState_SetAvailableClearsPollErr(t *testing.T) {
	s := newImportState()
	s.notePollError(errors.New("registry 503"))
	if snap := s.snapshot(); snap.LastPollErr == "" {
		t.Fatal("setup: expected LastPollErr to be populated")
	}

	s.setAvailable("v4") // poller recovered

	snap := s.snapshot()
	if snap.LastPollErr != "" {
		t.Errorf("LastPollErr must clear on successful poll; got %q", snap.LastPollErr)
	}
	if snap.AvailableVersion != "v4" {
		t.Errorf("AvailableVersion: got %q, want v4", snap.AvailableVersion)
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
