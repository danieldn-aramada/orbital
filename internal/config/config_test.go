package config

import (
	"log/slog"
	"os"
	"testing"
)

func TestNewConfig_EncryptionKeyValidation(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{
			name:    "empty key is allowed (disables encryption)",
			key:     "",
			wantErr: false,
		},
		{
			name:    "exactly 32 bytes is valid",
			key:     "12345678901234567890123456789012",
			wantErr: false,
		},
		{
			name:    "31 bytes is invalid",
			key:     "1234567890123456789012345678901",
			wantErr: true,
		},
		{
			name:    "33 bytes is invalid",
			key:     "123456789012345678901234567890123",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ORBITAL_SESSION_ENCRYPTION_KEY", tt.key)
			// Unset keys that would fail envconfig parsing on some envs.
			t.Setenv("ORBITAL_S3_RETENTION_COUNT", "30")

			_, err := New()
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestOCIConfigured(t *testing.T) {
	tests := []struct {
		name     string
		registry string
		keyPath  string
		want     bool
	}{
		{name: "both set", registry: "myregistry.azurecr.io", keyPath: "cosign.key", want: true},
		{name: "no registry", registry: "", keyPath: "cosign.key", want: false},
		{name: "no key path", registry: "myregistry.azurecr.io", keyPath: "", want: false},
		{name: "neither set", registry: "", keyPath: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				OCIRegistry:       tt.registry,
				OCISigningKeyPath: tt.keyPath,
			}
			if got := cfg.OCIConfigured(); got != tt.want {
				t.Errorf("OCIConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSlogLevel(t *testing.T) {
	tests := []struct {
		logLevel string
		want     slog.Level
	}{
		{logLevel: "debug", want: slog.LevelDebug},
		{logLevel: "info", want: slog.LevelInfo},
		{logLevel: "", want: slog.LevelInfo},
		{logLevel: "warn", want: slog.LevelWarn},
		{logLevel: "error", want: slog.LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.logLevel, func(t *testing.T) {
			cfg := &Config{LogLevel: tt.logLevel}
			if got := cfg.SlogLevel(); got != tt.want {
				t.Errorf("SlogLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAPIAuthResolution pins the acceptance list for ORBITAL_API_AUTH_ENABLED
// after ORBITAL_DEV was removed (2026-09-23).
//
// The regression this guards is someone re-coupling API auth to a mode flag, or
// flipping the unset default back to OFF. The old default was fail-OPEN: because
// auth followed !ORBITAL_DEV and that defaulted to true, an operator who
// configured nothing got NO API auth, and server.go's fail-closed guard could
// not catch it — that guard only fires when auth is required, and it wasn't.
func TestAPIAuthResolution(t *testing.T) {
	tests := []struct {
		name        string
		apiAuth     string // "" = leave ORBITAL_API_AUTH_ENABLED unset
		wantEnabled bool
		wantSource  string
		wantExplOff bool
	}{
		{
			name:        "unset — ENABLED, fail-safe (this is the behaviour change)",
			wantEnabled: true,
			wantSource:  "default (fail-safe)",
		},
		{
			name:        "explicit true — enabled",
			apiAuth:     "true",
			wantEnabled: true,
			wantSource:  "ORBITAL_API_AUTH_ENABLED",
		},
		{
			name:        "explicit false — disabled, and explicitly so",
			apiAuth:     "false",
			wantEnabled: false,
			wantSource:  "ORBITAL_API_AUTH_ENABLED",
			wantExplOff: true,
		},
		{
			name:        "unset is never an explicit disable",
			wantEnabled: true,
			wantSource:  "default (fail-safe)",
			wantExplOff: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ORBITAL_SESSION_HMAC_KEY", "test-hmac-key-not-the-placeholder")
			if tt.apiAuth != "" {
				t.Setenv("ORBITAL_API_AUTH_ENABLED", tt.apiAuth)
			} else {
				os.Unsetenv("ORBITAL_API_AUTH_ENABLED")
			}
			cfg, err := New()
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			if cfg.APIAuthEnabled != tt.wantEnabled {
				t.Errorf("APIAuthEnabled = %v, want %v", cfg.APIAuthEnabled, tt.wantEnabled)
			}
			if got := cfg.APIAuthSource(); got != tt.wantSource {
				t.Errorf("APIAuthSource() = %q, want %q", got, tt.wantSource)
			}
			if got := cfg.APIAuthExplicitlyDisabled(); got != tt.wantExplOff {
				t.Errorf("APIAuthExplicitlyDisabled() = %v, want %v", got, tt.wantExplOff)
			}
		})
	}
}

// TestTemplateHotReload_DefaultsOff pins the other half of the split: the
// convenience flag must not default on. ORBITAL_DEV defaulted to TRUE, so a
// deployment that set nothing got disk-reading handlers as well as no auth.
func TestTemplateHotReload_DefaultsOff(t *testing.T) {
	t.Setenv("ORBITAL_SESSION_HMAC_KEY", "test-hmac-key-not-the-placeholder")
	os.Unsetenv("ORBITAL_TEMPLATE_HOT_RELOAD_ENABLED")
	cfg, err := New()
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	if cfg.TemplateHotReload {
		t.Error("TemplateHotReload defaults ON — a convenience flag must be opt-in")
	}
}

// TestPlaceholderSessionKeyIsRefusedUnconditionally pins the third half. The
// literal below is published in this repository, so it is a secret nobody has.
// It used to be accepted whenever ORBITAL_DEV was true — the default — so the
// check protected only deployments that had already thought about it.
func TestPlaceholderSessionKeyIsRefusedUnconditionally(t *testing.T) {
	t.Setenv("ORBITAL_SESSION_HMAC_KEY", "local-dev-hmac-key-change-in-prod")
	os.Unsetenv("ORBITAL_TEMPLATE_HOT_RELOAD_ENABLED")
	if _, err := New(); err == nil {
		t.Fatal("the published placeholder HMAC key was accepted")
	}
	// And the negative: a real key is fine.
	t.Setenv("ORBITAL_SESSION_HMAC_KEY", "a-real-enough-local-key")
	if _, err := New(); err != nil {
		t.Fatalf("a non-placeholder key was refused: %v", err)
	}
}

func TestAPIAuthResolution_NonBooleanIsRefused(t *testing.T) {
	t.Setenv("ORBITAL_SESSION_HMAC_KEY", "test-hmac-key-not-the-placeholder")
	t.Setenv("ORBITAL_API_AUTH_ENABLED", "yes-please")
	if _, err := New(); err == nil {
		t.Fatal("expected an error for a non-boolean ORBITAL_API_AUTH_ENABLED, got nil")
	}
}
