package orbserver

import (
	"sync"
	"time"

	"github.com/armada/orbital/internal/orb"
)

// importState is the in-memory state shared between the import handler, the poller,
// and the status endpoint. A single mutex covers all fields.
type importState struct {
	mu               sync.RWMutex
	status           string // "idle" | "running" | "done" | "failed"
	currentVersion   string // tag of last successfully imported artifact
	availableVersion string // tag discovered by poller that is newer than currentVersion
	lastImport       *orb.ImportRecord
	lastError        string
	lastChecked      time.Time
}

func newImportState() *importState {
	return &importState{status: "idle"}
}

// hydrateFromHistory seeds in-memory state from the persisted import history
// at startup. Without this, restarting orb makes the status page show "Last
// Import: —" even though import-history.json on disk has entries. We seed from
// the most recent successful (Status == "done") record because failed imports
// don't represent applied state.
func (s *importState) hydrateFromHistory(records []orb.ImportRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.Status == "done" {
			rec := r
			s.lastImport = &rec
			s.currentVersion = r.Tag
			return
		}
	}
}

func (s *importState) setRunning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = "running"
	s.lastError = ""
}

func (s *importState) setDone(record orb.ImportRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = "done"
	s.currentVersion = record.Tag
	s.availableVersion = ""
	s.lastImport = &record
	s.lastError = ""
}

func (s *importState) setFailed(errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = "failed"
	s.lastError = errMsg
}

func (s *importState) setAvailable(tag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.availableVersion = tag
	s.lastChecked = time.Now().UTC()
}

func (s *importState) snapshot() importSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := importSnapshot{
		Status:           s.status,
		CurrentVersion:   s.currentVersion,
		AvailableVersion: s.availableVersion,
		LastError:        s.lastError,
		LastChecked:      s.lastChecked,
	}
	if s.lastImport != nil {
		r := *s.lastImport
		snap.LastImport = &r
	}
	return snap
}

type importSnapshot struct {
	Status           string             `json:"status"`
	CurrentVersion   string             `json:"currentVersion,omitempty"`
	AvailableVersion string             `json:"availableVersion,omitempty"`
	LastImport       *orb.ImportRecord  `json:"lastImport,omitempty"`
	LastError        string             `json:"lastError,omitempty"`
	LastChecked      time.Time          `json:"lastChecked,omitempty"`
}
