// Package db owns the Postgres connection pool and the credential that fills its
// password field. Nothing here issues a query beyond a health-check ping.
package db

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/jackc/pgx/v5"
)

// Credential supplies the Postgres password for each new connection.
//
// Two implementations, because orbital deploys to two very different places:
//
//   - AKS: Entra ID workload identity. The access token IS the password, scope
//     https://ossrdbms-aad.database.windows.net/.default, re-minted per connection.
//   - Local dev and air-gapped sites: minting a token requires reaching Entra ID,
//     which a disconnected deployment cannot do. A static password from the secret
//     store is the only option there, so StaticCredential is not a stopgap to
//     remove later.
type Credential interface {
	Password(ctx context.Context) (string, error)
	// MaxConnLifetime bounds a connection so it is retired before its credential expires.
	MaxConnLifetime() time.Duration
}

// StaticCredential is a password carried in the DSN or the secret store.
type StaticCredential struct{ Value string }

func (c StaticCredential) Password(context.Context) (string, error) { return c.Value, nil }
func (c StaticCredential) MaxConnLifetime() time.Duration           { return time.Hour }

// AzureMICredential mints Entra ID tokens for Azure Database for PostgreSQL.
type AzureMICredential struct {
	cred  azcore.TokenCredential
	scope string
}

// azureScope differs in Gov Cloud.
func azureScope() string {
	if os.Getenv("CLOUD_ENV") == "gov_cloud" {
		return "https://ossrdbms-aad.database.usgovcloudapi.net/.default"
	}
	return "https://ossrdbms-aad.database.windows.net/.default"
}

// NewAzureMICredential uses DefaultAzureCredential, which resolves to workload identity
// on AKS and to the IMDS endpoint on a VM without the caller choosing.
func NewAzureMICredential() (*AzureMICredential, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("creating azure credential: %w", err)
	}
	return &AzureMICredential{cred: cred, scope: azureScope()}, nil
}

func (c *AzureMICredential) Password(ctx context.Context) (string, error) {
	// azidentity caches and refreshes internally, so this is not a token-service
	// round trip per connection.
	token, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{c.scope}})
	if err != nil {
		return "", fmt.Errorf("fetching entra token: %w", err)
	}
	return token.Token, nil
}

// azureMIConnLifetime bounds a pooled connection's age so it is retired — and re-minted
// with a fresh token via beforeConnect — well before its Entra token can expire. It is
// deliberately a fixed value and NOT derived from a specific token's expiry: pgxpool
// reads MaxConnLifetime once at pool construction, before any token has been minted, so
// a per-token computation cannot run. A fixed value comfortably below Entra ID's
// access-token lifetime is the honest shape.
const azureMIConnLifetime = 30 * time.Minute

func (c *AzureMICredential) MaxConnLifetime() time.Duration { return azureMIConnLifetime }

// beforeConnect re-reads the credential for every new connection, so a rotated token is
// picked up without a restart.
func beforeConnect(cred Credential) func(context.Context, *pgx.ConnConfig) error {
	return func(ctx context.Context, cfg *pgx.ConnConfig) error {
		password, err := cred.Password(ctx)
		if err != nil {
			return fmt.Errorf("resolving database credential: %w", err)
		}
		cfg.Password = password
		return nil
	}
}

// CredentialFor picks the credential source: managed identity where the platform
// provides it, the password already in the DSN where it cannot.
func CredentialFor(useAzureMI bool, password string) (Credential, error) {
	if useAzureMI {
		return NewAzureMICredential()
	}
	return StaticCredential{Value: password}, nil
}
