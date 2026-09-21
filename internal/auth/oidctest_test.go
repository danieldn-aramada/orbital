package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// Shared test fixtures for every bearer-verifying path in this package. They
// lived in bearer_test.go until the single-issuer verifier was removed.

// newTestOIDCServer starts a minimal OIDC provider backed by a fresh RSA key.
// Returns the issuer URL and a helper that signs JWTs with that key.
func newTestOIDCServer(t *testing.T) (issuerURL string, sign func(claims map[string]any) string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	const kid = "test-key-1"

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/auth",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &key.PublicKey
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"keys": []map[string]any{{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": kid,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})

	sign = func(claims map[string]any) string {
		hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
		pay, _ := json.Marshal(claims)
		h := base64.RawURLEncoding.EncodeToString(hdr)
		p := base64.RawURLEncoding.EncodeToString(pay)
		digest := sha256.Sum256([]byte(h + "." + p))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatalf("sign JWT: %v", err)
		}
		return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig)
	}

	return srv.URL, sign
}

func echoCtx(req *http.Request) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}
