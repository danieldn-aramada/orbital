package handler

import (
	"errors"

	"github.com/armada/orbital/internal/bundler"
	"github.com/armada/orbital/internal/metrics"
)

// jobFailureReason classifies a job error into the closed metric label set.
//
// Structural only — errors.Is against sentinels, never a substring of the
// message. A label derived from prose reclassifies itself the day someone
// rewords an error, and nothing announces that the panel now means something
// different.
//
// Unrecognised errors are ReasonInternal, which reads as "orbital has not
// classified this yet" rather than as a claim about the cause. That is honest,
// and a rising internal count is itself the signal to add a sentinel.
func jobFailureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, bundler.ErrUnreachable):
		return metrics.ReasonDependencyUnreachable
	case errors.Is(err, bundler.ErrRejected):
		return metrics.ReasonDependencyRejected
	default:
		return metrics.ReasonInternal
	}
}
