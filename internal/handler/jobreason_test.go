package handler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/armada/orbital/internal/bundler"
	"github.com/armada/orbital/internal/metrics"
)

// Acceptance 6: the reason is derived STRUCTURALLY, from sentinels, never from
// the text of the error.
//
// The regression this guards is someone "simplifying" jobFailureReason into a
// strings.Contains over the message. That passes every obvious test and then
// silently reclassifies the day an error is reworded — the panel keeps showing
// a reason, just the wrong one, and nothing says so.
func TestJobFailureReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil is not a failure", nil, ""},
		{"unreachable bundler", fmt.Errorf("wrapped: %w",
			bundler.ClassifyForTest(bundler.ErrUnreachable, errors.New("call bundler: dial tcp: refused"))),
			metrics.ReasonDependencyUnreachable},
		{"bundler answered with an error", fmt.Errorf("publish: %w",
			bundler.ClassifyForTest(bundler.ErrRejected, errors.New("bundler returned HTTP 500: oauth2 ..."))),
			metrics.ReasonDependencyRejected},
		{"anything unclassified is internal, not a guess",
			errors.New("some new failure nobody has classified"), metrics.ReasonInternal},
		// The text ALONE must not classify. This error reads exactly like the
		// real thing and carries no sentinel, so a substring implementation
		// would call it dependency_rejected and this test would catch it.
		{"a message that merely LOOKS like a bundler rejection",
			errors.New("bundler returned HTTP 500: something"), metrics.ReasonInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jobFailureReason(tc.err); got != tc.want {
				t.Errorf("jobFailureReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
