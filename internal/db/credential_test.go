package db

import (
	"testing"
	"time"
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
