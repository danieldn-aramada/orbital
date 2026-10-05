//go:build integration

package handler

import (
	"context"
	"log/slog"
	"testing"

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/testutil"
)

// liveViewSet resolves the shipped views config against a running DGraph.
func liveViewSet(t *testing.T, dgraphURL string) configitems.ViewSet {
	t.Helper()
	sf := NewSharedFieldSource(dgraphURL, ViewsSource{Path: testutil.ViewsPath()}, slog.Default())
	vs, err := sf.ViewSet(context.Background())
	if err != nil {
		t.Fatalf("resolve the shipped views against %s: %v", dgraphURL, err)
	}
	return vs
}

// liveViews is liveViewSet as a ViewsProvider, for handlers that take one.
func liveViews(t *testing.T, dgraphURL string) ViewsProvider {
	vs := liveViewSet(t, dgraphURL)
	return func(context.Context) (configitems.ViewSet, error) { return vs, nil }
}
