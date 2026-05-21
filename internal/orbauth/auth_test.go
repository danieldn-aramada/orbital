package orbauth

import (
	"testing"
	"time"
)

// ── JWTDisplayClaims ──────────────────────────────────────────────────────────

func TestJWTDisplayClaims(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		wantName  string
		wantEmail string
	}{
		{
			name:      "preferred_username used when present",
			token:     "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJuYW1lIjogIkFsaWNlIFNtaXRoIiwgInByZWZlcnJlZF91c2VybmFtZSI6ICJhbGljZUBhcm1hZGEuYWkifQ.ZmFrZXNpZw",
			wantName:  "Alice Smith",
			wantEmail: "alice@armada.ai",
		},
		{
			name:      "upn used when preferred_username absent",
			token:     "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJuYW1lIjogIkJvYiIsICJ1cG4iOiAiYm9iQGFybWFkYS5haSJ9.ZmFrZXNpZw",
			wantName:  "Bob",
			wantEmail: "bob@armada.ai",
		},
		{
			name:      "malformed token returns unknown and empty email",
			token:     "notavalidtoken",
			wantName:  "unknown",
			wantEmail: "",
		},
		{
			name:      "two-part token returns unknown",
			token:     "header.payload",
			wantName:  "unknown",
			wantEmail: "",
		},
		{
			name:      "invalid base64 payload returns unknown",
			token:     "header.!!!.sig",
			wantName:  "unknown",
			wantEmail: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotEmail := JWTDisplayClaims(tt.token)
			if gotName != tt.wantName {
				t.Errorf("name: got %q, want %q", gotName, tt.wantName)
			}
			if gotEmail != tt.wantEmail {
				t.Errorf("email: got %q, want %q", gotEmail, tt.wantEmail)
			}
		})
	}
}

// ── Credentials.Valid ─────────────────────────────────────────────────────────

func TestCredentials_Valid(t *testing.T) {
	t.Run("valid when expires more than 60 seconds away", func(t *testing.T) {
		c := &Credentials{ExpiresAt: time.Now().Add(2 * time.Minute)}
		if !c.Valid() {
			t.Error("expected Valid() = true")
		}
	})

	t.Run("invalid when expires in less than 60 seconds", func(t *testing.T) {
		c := &Credentials{ExpiresAt: time.Now().Add(30 * time.Second)}
		if c.Valid() {
			t.Error("expected Valid() = false")
		}
	})

	t.Run("invalid when already expired", func(t *testing.T) {
		c := &Credentials{ExpiresAt: time.Now().Add(-1 * time.Minute)}
		if c.Valid() {
			t.Error("expected Valid() = false")
		}
	})

	t.Run("invalid when exactly at 60-second boundary", func(t *testing.T) {
		c := &Credentials{ExpiresAt: time.Now().Add(60 * time.Second)}
		if c.Valid() {
			t.Error("expected Valid() = false at boundary")
		}
	})
}

// ── RandomString ──────────────────────────────────────────────────────────────

func TestRandomString(t *testing.T) {
	t.Run("returns string of requested length", func(t *testing.T) {
		for _, n := range []int{1, 16, 32, 64} {
			s, err := RandomString(n)
			if err != nil {
				t.Fatalf("n=%d: unexpected error: %v", n, err)
			}
			if len(s) != n {
				t.Errorf("n=%d: got len %d", n, len(s))
			}
		}
	})

	t.Run("returns only URL-safe characters", func(t *testing.T) {
		s, err := RandomString(256)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, c := range s {
			if !isURLSafe(c) {
				t.Errorf("non-URL-safe character %q in output", c)
			}
		}
	})

	t.Run("two calls produce different strings", func(t *testing.T) {
		a, _ := RandomString(32)
		b, _ := RandomString(32)
		if a == b {
			t.Error("expected two random strings to differ")
		}
	})
}

func isURLSafe(r rune) bool {
	return (r >= 'A' && r <= 'Z') ||
		(r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_'
}
