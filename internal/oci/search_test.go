package oci

import (
	"testing"
	"time"
)

func TestParseImageMeta(t *testing.T) {
	t.Run("both timestamps", func(t *testing.T) {
		m, err := parseImageMeta([]byte(`{"data":{"Image":{"LastUpdated":"2026-08-03T20:00:00Z","PushTimestamp":"2026-08-03T20:00:08Z"}}}`))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if m.LastUpdated.IsZero() || m.PushTimestamp.IsZero() {
			t.Fatalf("expected both set, got %+v", m)
		}
		if d := m.PushTimestamp.Sub(m.LastUpdated); d != 8*time.Second {
			t.Errorf("push−lastUpdated = %v, want 8s", d)
		}
	})

	t.Run("zero-sentinel push collapses to zero time", func(t *testing.T) {
		m, err := parseImageMeta([]byte(`{"data":{"Image":{"LastUpdated":"2026-08-03T20:00:00Z","PushTimestamp":"0001-01-01T00:00:00Z"}}}`))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !m.PushTimestamp.IsZero() {
			t.Errorf("zero-sentinel push should be zero time, got %v", m.PushTimestamp)
		}
		if m.LastUpdated.IsZero() {
			t.Error("lastUpdated should be set")
		}
	})

	t.Run("graphql errors payload is an error", func(t *testing.T) {
		if _, err := parseImageMeta([]byte(`{"errors":[{"message":"repo not found"}]}`)); err == nil {
			t.Error("expected error for errors[] payload")
		}
	})

	t.Run("malformed json is an error", func(t *testing.T) {
		if _, err := parseImageMeta([]byte(`not json`)); err == nil {
			t.Error("expected error for malformed json")
		}
	})

	t.Run("empty Image yields zeros, no error", func(t *testing.T) {
		m, err := parseImageMeta([]byte(`{"data":{"Image":{}}}`))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !m.PushTimestamp.IsZero() || !m.LastUpdated.IsZero() {
			t.Errorf("empty Image should yield zero times, got %+v", m)
		}
	})
}
