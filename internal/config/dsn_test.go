package config

import "testing"

// Under managed identity the DSN must carry NO password — internal/db's BeforeConnect
// hook fills it per connection with a freshly minted Entra token. A password baked in
// here would be used instead and silently defeat token rotation.
func TestDatabaseDSNUnderManagedIdentityHasNoPassword(t *testing.T) {
	c := &Config{
		DBUseAzMI: true,
		DBHost:    "pg-applz-devcc-westus3.postgres.database.azure.com",
		DBPort:    5432,
		DBUser:    "id-armada-orbital-dev-cc-wus-01",
		DBName:    "armada-orbital",
		DBSSLMode: "require",
	}
	want := "postgres://id-armada-orbital-dev-cc-wus-01@pg-applz-devcc-westus3.postgres.database.azure.com:5432/armada-orbital?sslmode=require"
	if got := c.DatabaseDSN(); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// With managed identity off, DATABASE_URL is passed through untouched — this is the
// local-dev and air-gapped path, where the password is already in the string.
func TestDatabaseDSNFallsBackToDatabaseURL(t *testing.T) {
	c := &Config{DBUseAzMI: false, DatabaseURL: "postgres://orbital:pw@localhost:5432/orbital?sslmode=disable"}
	if got := c.DatabaseDSN(); got != c.DatabaseURL {
		t.Fatalf("got %q, want passthrough of %q", got, c.DatabaseURL)
	}
}
