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

// external-jwt assigns ORBITAL_JWT_DEFAULT_ROLE to every valid bearer token
// rather than reading a per-user role, so the default must be the LEAST
// privileged tier. This fails if someone changes the default to dev/admin —
// a change with no visible symptom at runtime, and one that would silently
// grant write access to every valid token.
func TestExternalJWT_DefaultRoleIsLeastPrivilege(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		wantErr bool
	}{
		{"explicitly empty is refused — a config mistake, not a fallback", "", true},
		{"readonly is accepted", "readonly", false},
		{"dev is accepted", "dev", false},
		{"admin is accepted", "admin", false},
		{"garbage is refused", "superuser", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ORBITAL_AUTH_MODE", "external-jwt")
			t.Setenv("ORBITAL_JWT_ISSUER", "https://keycloak.example.com/realms/x")
			t.Setenv("ORBITAL_JWT_AUDIENCE", "account")
			t.Setenv("ORBITAL_JWT_CLIENT_ID", "some-client")
			t.Setenv("ORBITAL_JWT_DEFAULT_ROLE", tt.role)

			_, err := New()
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}

	// The load-bearing case: the var is absent entirely, which is what a
	// deployment that never thought about it looks like.
	t.Run("absent falls back to least privilege", func(t *testing.T) {
		t.Setenv("ORBITAL_AUTH_MODE", "external-jwt")
		t.Setenv("ORBITAL_JWT_ISSUER", "https://keycloak.example.com/realms/x")
		t.Setenv("ORBITAL_JWT_AUDIENCE", "account")
		t.Setenv("ORBITAL_JWT_CLIENT_ID", "some-client")
		t.Setenv("ORBITAL_JWT_DEFAULT_ROLE", "placeholder") // registers cleanup
		os.Unsetenv("ORBITAL_JWT_DEFAULT_ROLE")

		cfg, err := New()
		if err != nil {
			t.Fatalf("New() error = %v, want nil", err)
		}
		if cfg.JWTDefaultRole != "readonly" {
			t.Errorf("absent default = %q, want readonly — a default that grants writes is an authorization decision nobody made", cfg.JWTDefaultRole)
		}
	})
}

// The role is read only in external-jwt mode, so leaving it unset must NOT
// break every other deployment — the negative half of the rule above.
func TestDefaultRoleUnsetIsFineOutsideExternalJWT(t *testing.T) {
	t.Setenv("ORBITAL_AUTH_MODE", "")
	t.Setenv("ORBITAL_JWT_DEFAULT_ROLE", "")

	if _, err := New(); err != nil {
		t.Errorf("New() error = %v, want nil — the role is external-jwt-only", err)
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
