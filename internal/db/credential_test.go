package db

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// An Entra access token lives ~60 minutes. A pooled connection is retired at
// MaxConnLifetime (plus jitter), so this must stay comfortably below that or a
// connection outlives the token it authenticated with — which fails at query time,
// not at connect time, and so reads as an intermittent database fault.
func TestAzureMIConnLifetimeIsBelowTokenLifetime(t *testing.T) {
	c := &AzureMICredential{}
	if got := c.MaxConnLifetime(); got >= 60*time.Minute {
		t.Fatalf("MaxConnLifetime %v must stay well below the ~60m Entra token lifetime", got)
	}
}

func TestStaticCredentialReturnsItsValue(t *testing.T) {
	got, err := StaticCredential{Value: "hunter2"}.Password(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q, want %q", got, "hunter2")
	}
}

// CredentialFor must not reach for Azure when managed identity is off — that path
// runs on a laptop and in air-gapped sites where minting a token is impossible.
func TestCredentialForStaticDoesNotRequireAzure(t *testing.T) {
	cred, err := CredentialFor(false, "hunter2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cred.(StaticCredential); !ok {
		t.Fatalf("got %T, want StaticCredential", cred)
	}
}

// The regression this guards: orbital keeps its local-dev password inside
// DATABASE_URL, not in a separate field, so CredentialFor(false, "") yields an empty
// static password. An earlier version assigned that unconditionally and wiped the
// DSN's own credential — `make run-orbital` then died with
// "password authentication failed for user \"orbital\"".
func TestBeforeConnectLeavesDSNPasswordWhenCredentialIsEmpty(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://orbital:orbital-local-dev-secret@localhost:5432/orbital?sslmode=disable")
	if err != nil {
		t.Fatalf("parsing dsn: %v", err)
	}
	if err := beforeConnect(StaticCredential{Value: ""})(t.Context(), cfg); err != nil {
		t.Fatalf("beforeConnect: %v", err)
	}
	if cfg.Password != "orbital-local-dev-secret" {
		t.Fatalf("password was clobbered: got %q", cfg.Password)
	}
}

// A non-empty credential must win — that is the managed-identity path, where the DSN
// deliberately carries no password.
func TestBeforeConnectAppliesNonEmptyCredential(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://id-armada-orbital@example.postgres.database.azure.com:5432/armada-orbital?sslmode=require")
	if err != nil {
		t.Fatalf("parsing dsn: %v", err)
	}
	if err := beforeConnect(StaticCredential{Value: "minted-token"})(t.Context(), cfg); err != nil {
		t.Fatalf("beforeConnect: %v", err)
	}
	if cfg.Password != "minted-token" {
		t.Fatalf("got %q, want %q", cfg.Password, "minted-token")
	}
}
