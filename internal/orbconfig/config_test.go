package orbconfig_test

import (
	"testing"

	"github.com/armada/orbital/internal/orbconfig"
)

func TestConsumersConfig_ParsesJSON(t *testing.T) {
	var c orbconfig.ConsumersConfig
	if err := c.Decode(`[{"mediaType":"application/vnd.armada.configbundle.manifest.v1+yaml","url":"http://cb:8080/consume"}]`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c) != 1 {
		t.Fatalf("expected 1 consumer, got %d", len(c))
	}
	if c[0].MediaType != "application/vnd.armada.configbundle.manifest.v1+yaml" {
		t.Errorf("mediaType mismatch: %q", c[0].MediaType)
	}
	if c[0].URL != "http://cb:8080/consume" {
		t.Errorf("url mismatch: %q", c[0].URL)
	}
}

func TestConsumersConfig_Empty(t *testing.T) {
	var c orbconfig.ConsumersConfig
	if err := c.Decode(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c != nil {
		t.Errorf("expected nil slice for empty string, got %v", c)
	}
}

func TestConsumersConfig_Invalid(t *testing.T) {
	var c orbconfig.ConsumersConfig
	if err := c.Decode("not json"); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestConsumersConfig_Multiple(t *testing.T) {
	var c orbconfig.ConsumersConfig
	if err := c.Decode(`[{"mediaType":"a/b","url":"http://x"},{"mediaType":"c/d","url":"http://y"}]`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c) != 2 {
		t.Fatalf("expected 2 consumers, got %d", len(c))
	}
	if c[0].MediaType != "a/b" || c[1].MediaType != "c/d" {
		t.Errorf("unexpected media types: %v", c)
	}
}
