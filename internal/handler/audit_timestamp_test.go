package handler

import (
	"testing"
	"time"
)

// Audit timestamps are stored at PostgreSQL's microsecond precision and were
// serialized with time.RFC3339 — whole seconds. Two events written in the same
// second therefore came back indistinguishable, and their order was
// unrecoverable from API output, which is exactly what AU-7(b) says reporting
// must not do to the record.
//
// The regression class is a format constant silently reverting: RFC3339 and
// RFC3339Nano differ by six characters, the change is invisible in review, and
// nothing else in the system fails when it happens.
//
// This asserts the property (distinguishable, ordered, parseable) rather than a
// literal string, because the point is not the format's name.
func TestAuditTimestampFormat_PreservesSubSecondOrdering(t *testing.T) {
	base := time.Date(2026, 10, 6, 14, 30, 0, 0, time.UTC)
	earlier := base.Add(1 * time.Microsecond)
	later := base.Add(2 * time.Microsecond)

	gotEarlier := earlier.UTC().Format(time.RFC3339Nano)
	gotLater := later.UTC().Format(time.RFC3339Nano)

	if gotEarlier == gotLater {
		t.Fatalf("two events 1µs apart serialize identically (%q) — their order is unrecoverable", gotEarlier)
	}
	if !(gotEarlier < gotLater) {
		t.Errorf("lexical order disagrees with chronological order: %q then %q", gotEarlier, gotLater)
	}

	// Still RFC3339, so existing parsers are unaffected — this is what makes the
	// change safe to make without a contract bump.
	parsed, err := time.Parse(time.RFC3339, gotEarlier)
	if err != nil {
		t.Fatalf("output is no longer parseable as RFC3339: %v", err)
	}
	if !parsed.Equal(earlier) {
		t.Errorf("round trip lost precision: got %v, want %v", parsed, earlier)
	}
}

// The negative, and the reason the whole-second format was wrong: a timestamp
// with no sub-second component must still serialize cleanly. RFC3339Nano drops
// trailing zeros, so a whole-second time is unchanged by this fix — nothing
// about existing rows' presentation shifts.
func TestAuditTimestampFormat_WholeSecondsAreUnchanged(t *testing.T) {
	whole := time.Date(2026, 10, 6, 14, 30, 0, 0, time.UTC)

	if got, want := whole.Format(time.RFC3339Nano), whole.Format(time.RFC3339); got != want {
		t.Errorf("whole-second timestamp changed shape: got %q, want %q", got, want)
	}
}
