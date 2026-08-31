package handler

import (
	"testing"

	"github.com/armada/orbital/internal/approval"
)

// The ?status= filter went from one value to a repeatable, OR-ed set so the
// queue's "Closed" tab could mean merged OR rejected OR withdrawn.
//
// These are pure functions with the edge cases that matter — an empty filter, a
// value that maps to a DERIVED status rather than a stored one, and duplicates
// that must collapse to one predicate. The regression class is the one this
// endpoint has already been bitten by twice: a repeatable parameter read as a
// single value answers about the first entry alone and looks correct doing it.

func TestValidStatusFilter(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"stored terminal status", approval.StatusMerged, true},
		{"stored open status", approval.StatusOpen, true},
		{"derived status", approval.StatusApproved, true},
		{"filter-only aggregate", statusActive, true},
		{"wrong case is not accepted", "Merged", false},
		{"unknown word", "abandoned", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validStatusFilter(tt.value); got != tt.want {
				t.Errorf("validStatusFilter(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestStoredStatePredicates(t *testing.T) {
	tests := []struct {
		name   string
		wanted []string
		want   int
	}{
		{"no filter narrows nothing", nil, 0},
		{"one terminal status", []string{approval.StatusMerged}, 1},
		// The queue's Closed tab. Three predicates OR-ed, not one — reading the
		// parameter as a single value would silently ask about `merged` alone.
		{"every terminal status", []string{approval.StatusMerged, approval.StatusRejected, approval.StatusClosed}, 3},
		// open, approved and active all live in the stored `open` row, so they
		// collapse: `approved` is derived from the approval count, and SQL can
		// only narrow to non-terminal.
		{"non-terminal values collapse to one", []string{approval.StatusOpen, approval.StatusApproved, statusActive}, 1},
		{"mixed", []string{approval.StatusOpen, approval.StatusMerged}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(storedStatePredicates(tt.wanted)); got != tt.want {
				t.Errorf("storedStatePredicates(%v) built %d predicates, want %d", tt.wanted, got, tt.want)
			}
		})
	}
}

func TestStatusWanted(t *testing.T) {
	tests := []struct {
		name    string
		wanted  []string
		derived string
		want    bool
	}{
		{"no filter keeps everything", nil, approval.StatusMerged, true},
		{"exact match", []string{approval.StatusMerged}, approval.StatusMerged, true},
		// Both share the stored `open` row, so SQL cannot tell them apart and
		// this is the only place the distinction is made.
		{"open excludes approved", []string{approval.StatusOpen}, approval.StatusApproved, false},
		{"approved excludes open", []string{approval.StatusApproved}, approval.StatusOpen, false},
		{"active accepts open", []string{statusActive}, approval.StatusOpen, true},
		{"active accepts approved", []string{statusActive}, approval.StatusApproved, true},
		{"active rejects terminal", []string{statusActive}, approval.StatusClosed, false},
		// The union is the point: each member of the Closed tab's filter has to
		// survive it, not just the first.
		{"union keeps merged", []string{approval.StatusMerged, approval.StatusRejected, approval.StatusClosed}, approval.StatusMerged, true},
		{"union keeps rejected", []string{approval.StatusMerged, approval.StatusRejected, approval.StatusClosed}, approval.StatusRejected, true},
		{"union keeps closed", []string{approval.StatusMerged, approval.StatusRejected, approval.StatusClosed}, approval.StatusClosed, true},
		{"union excludes open", []string{approval.StatusMerged, approval.StatusRejected, approval.StatusClosed}, approval.StatusOpen, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusWanted(tt.wanted, tt.derived); got != tt.want {
				t.Errorf("statusWanted(%v, %q) = %v, want %v", tt.wanted, tt.derived, got, tt.want)
			}
		})
	}
}
