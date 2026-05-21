package config

import (
	"log/slog"
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
