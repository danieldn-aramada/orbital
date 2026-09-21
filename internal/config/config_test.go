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

// TestAPIAuthResolution pins the acceptance list for splitting API auth out of
// ORBITAL_DEV. One case per item: the hierarchy is resolved once here, and the
// regression it guards is someone re-coupling API auth to Dev (or flipping the
// unset default, which would silently change every existing deployment).
func TestAPIAuthResolution(t *testing.T) {
	tests := []struct {
		name        string
		dev         string
		apiAuth     string // "" = leave ORBITAL_API_AUTH_ENABLED unset
		wantEnabled bool
		wantSource  string
		wantExplOff bool
	}{
		{
			name:        "item 1: dev=true, unset — disabled, matching historical default",
			dev:         "true",
			wantEnabled: false,
			wantSource:  "ORBITAL_DEV",
		},
		{
			name:        "item 2: dev=false, unset — enabled, matching historical default",
			dev:         "false",
			wantEnabled: true,
			wantSource:  "ORBITAL_DEV",
		},
		{
			name:        "item 3: dev=true + explicit true — enabled, hot-reload retained",
			dev:         "true",
			apiAuth:     "true",
			wantEnabled: true,
			wantSource:  "ORBITAL_API_AUTH_ENABLED",
		},
		{
			name:        "item 4: dev=false + explicit false — disabled, and explicitly so",
			dev:         "false",
			apiAuth:     "false",
			wantEnabled: false,
			wantSource:  "ORBITAL_API_AUTH_ENABLED",
			wantExplOff: true,
		},
		{
			name:        "inherited false is not an explicit disable",
			dev:         "true",
			wantEnabled: false,
			wantSource:  "ORBITAL_DEV",
			wantExplOff: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ORBITAL_DEV", tt.dev)
			// Dev=false refuses the placeholder HMAC key; unrelated to this test.
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

func TestAPIAuthResolution_NonBooleanIsRefused(t *testing.T) {
	t.Setenv("ORBITAL_DEV", "true")
	t.Setenv("ORBITAL_API_AUTH_ENABLED", "yes-please")
	if _, err := New(); err == nil {
		t.Fatal("expected an error for a non-boolean ORBITAL_API_AUTH_ENABLED, got nil")
	}
}
