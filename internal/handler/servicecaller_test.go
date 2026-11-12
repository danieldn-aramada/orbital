package handler

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/labstack/echo/v4"
)

// An app principal — a client-credentials caller — takes its role from its
// provider entry and is never a user. These run the real verifier, ResolveUser,
// RequireRole and GraphQL handler in the order server.go wires them. Names
// transcribed from the machine-principal acceptance list (items 1, 5, 8).
//
// db is closedClient: non-nil, so authorization is enforced rather than skipped
// as in dev mode, and any users-table access fails instead of passing silently.

func newServiceIssuer(t *testing.T) (string, func(map[string]any) string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	const kid = "service-test-key"
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"keys": []map[string]any{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}},
		})
	})

	sign := func(claims map[string]any) string {
		hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
		pay, _ := json.Marshal(claims)
		h := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(pay)
		digest := sha256.Sum256([]byte(h))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatalf("sign JWT: %v", err)
		}
		return h + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	return srv.URL, sign
}

func serviceTokenClaims(iss string, extra map[string]any) map[string]any {
	now := time.Now()
	cl := map[string]any{
		"iss": iss, "aud": []any{"armada-orbital"},
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
	}
	for k, v := range extra {
		cl[k] = v
	}
	return cl
}

// A Keycloak client-credentials token: a client claim, preferred_username, no email.
func clientCredentialsToken(iss, client string) map[string]any {
	return serviceTokenClaims(iss, map[string]any{
		"sub": "kc-sub-" + client, "azp": client, "client_id": client,
		"preferred_username": "service-account-" + client,
	})
}

func firmwareClientSpec(iss string) auth.ProviderSpec {
	return auth.ProviderSpec{
		IssuerURL: iss, ClientID: "armada-firmware-svc", Audiences: []string{"armada-orbital"},
		UsernameClaim: "preferred_username", DelegatedRole: "readonly",
	}
}

type serviceStack struct {
	e *echo.Echo

	mu         sync.Mutex
	dgraphOps  []string
	lastUserID any
}

func newServiceStack(t *testing.T, db *ent.Client, specs ...auth.ProviderSpec) *serviceStack {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &serviceStack{e: echo.New()}

	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		_ = json.Unmarshal(body, &req)
		kind := "query"
		if isMutation(req.Query) {
			kind = "mutation"
		}
		s.mu.Lock()
		s.dgraphOps = append(s.dgraphOps, kind)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(dgraph.Close)

	ps, err := auth.NewProviderSet(context.Background(), specs, logger,
		auth.WithAuditSink(BearerAuthFailureSink(db, logger)))
	if err != nil {
		t.Fatalf("NewProviderSet: %v", err)
	}
	apiAuth := []echo.MiddlewareFunc{ps.RequireAuth(), auth.RequireCSRFOnCookieAuth(), ResolveUser(db, nil)}

	reached := func(c echo.Context) error {
		s.mu.Lock()
		s.lastUserID = c.Get("user_id")
		s.mu.Unlock()
		return c.NoContent(http.StatusOK)
	}
	api := s.e.Group("/api/v1", append(apiAuth, RequireRole(db, user.RoleDev))...)
	api.POST("/restore", reached)
	api.POST("/backup", reached)
	api.POST("/export", reached)
	api.GET("/divergences", reached)

	gql := NewGraphQL(dgraph.URL, db, logger, false)
	s.e.Group("", apiAuth...).POST("/graphql", gql.Handle)
	return s
}

func (s *serviceStack) do(t *testing.T, method, path, token, body string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec.Code
}

func (s *serviceStack) dgraphSaw(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range s.dgraphOps {
		if k == kind {
			n++
		}
	}
	return n
}

const (
	gqlQueryBody    = `{"query":"{ queryDataCenter { id name } }"}`
	gqlMutationBody = `{"query":"mutation { deleteDataCenter(filter: {}) { numUids } }"}`
)

func TestServiceCaller_DelegatedReadonlyClientIsRefusedEveryWriteAndCanRead(t *testing.T) {
	iss, sign := newServiceIssuer(t)
	s := newServiceStack(t, closedClient(t), firmwareClientSpec(iss))
	token := sign(clientCredentialsToken(iss, "armada-firmware-svc"))

	for _, path := range []string{"/api/v1/restore", "/api/v1/backup", "/api/v1/export"} {
		if code := s.do(t, http.MethodPost, path, token, `{}`); code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403 — the entry says readonly", path, code)
		}
	}
	if code := s.do(t, http.MethodPost, "/graphql", token, gqlMutationBody); code != http.StatusForbidden {
		t.Errorf("GraphQL mutation = %d, want 403", code)
	}
	if n := s.dgraphSaw("mutation"); n != 0 {
		t.Errorf("DGraph received %d mutation(s) from a readonly service", n)
	}

	if code := s.do(t, http.MethodGet, "/api/v1/divergences", token, ""); code != http.StatusOK {
		t.Errorf("GET /api/v1/divergences = %d, want 200 — readonly may read", code)
	}
	if code := s.do(t, http.MethodPost, "/graphql", token, gqlQueryBody); code != http.StatusOK {
		t.Errorf("GraphQL query = %d, want 200 — readonly may read", code)
	}
	if n := s.dgraphSaw("query"); n != 1 {
		t.Errorf("DGraph received %d queries, want 1", n)
	}
}

func TestServiceCaller_NeverReadsOrWritesTheUsersTable(t *testing.T) {
	iss, sign := newServiceIssuer(t)
	people := auth.ProviderSpec{IssuerURL: iss, Audiences: []string{"armada-orbital"}, UsernameClaim: "email", DefaultRole: "readonly"}
	s := newServiceStack(t, closedClient(t), firmwareClientSpec(iss), people)

	token := sign(clientCredentialsToken(iss, "armada-firmware-svc"))
	if code := s.do(t, http.MethodGet, "/api/v1/divergences", token, ""); code != http.StatusOK {
		t.Fatalf("got %d, want 200 — a users-table lookup or provision was attempted", code)
	}
	if s.lastUserID != nil {
		t.Errorf("user_id = %v, want unset — an app principal is not a user", s.lastUserID)
	}

	// Positive control: a person on the same chain does reach the users table,
	// so the closed database refuses them. Without this, a ResolveUser that
	// stopped provisioning anyone would pass the checks above.
	person := serviceTokenClaims(iss, map[string]any{"sub": "p1", "email": "person@example.com"})
	if code := s.do(t, http.MethodGet, "/api/v1/divergences", sign(person), ""); code != http.StatusUnauthorized {
		t.Errorf("person got %d, want 401 from the unreachable users table", code)
	}
}

func TestServiceCaller_BundlerReadsWithItsEntrysReadonlyFloor(t *testing.T) {
	iss, sign := newServiceIssuer(t)
	armadaOrbital := auth.ProviderSpec{
		IssuerURL: iss, ClientID: "armada-orbital", Audiences: []string{"armada-orbital"},
		SelfClientID: "armada-orbital", UsernameClaim: "email", GroupsClaim: "orbital_roles",
		RoleMapping: []struct{ Group, Role string }{{Group: "orbital-admin", Role: "admin"}},
		DefaultRole: "readonly",
	}
	s := newServiceStack(t, closedClient(t), armadaOrbital)
	token := sign(clientCredentialsToken(iss, "armada-orbital"))

	if code := s.do(t, http.MethodPost, "/graphql", token, gqlQueryBody); code != http.StatusOK {
		t.Errorf("GraphQL query = %d, want 200", code)
	}
	if n := s.dgraphSaw("query"); n != 1 {
		t.Errorf("DGraph received %d queries, want 1", n)
	}
	if code := s.do(t, http.MethodGet, "/api/v1/divergences", token, ""); code != http.StatusOK {
		t.Errorf("GET /api/v1/divergences = %d, want 200", code)
	}
	if code := s.do(t, http.MethodPost, "/graphql", token, gqlMutationBody); code != http.StatusForbidden {
		t.Errorf("GraphQL mutation = %d, want 403 — the bundler holds the entry's readonly floor, not dev", code)
	}
}

// A declared app entry (principalType "app") for a Kubernetes cluster issuer.
// Names transcribed from the declared-app-principal acceptance list (items 2, 3).

const firmwareSA = "system:serviceaccount:firmware:firmware-svc"

// A Kubernetes ServiceAccount token: a dedicated audience, no email, no client claim.
func serviceAccountToken(iss, sub string) map[string]any {
	return serviceTokenClaims(iss, map[string]any{"sub": sub, "aud": []any{"orbital"}})
}

func clusterEntry(iss string) auth.ProviderSpec {
	return auth.ProviderSpec{
		IssuerURL: iss, Audiences: []string{"orbital"}, App: true,
		UsernameClaim: "sub", GroupsClaim: "sub",
		RoleMapping: []struct{ Group, Role string }{{Group: firmwareSA, Role: "readonly"}},
	}
}

func TestAppEntry_ReadonlyServiceAccountReadsAndIsRefusedEveryWrite(t *testing.T) {
	iss, sign := newServiceIssuer(t)
	s := newServiceStack(t, closedClient(t), clusterEntry(iss))
	token := sign(serviceAccountToken(iss, firmwareSA))

	for _, path := range []string{"/api/v1/restore", "/api/v1/backup", "/api/v1/export"} {
		if code := s.do(t, http.MethodPost, path, token, `{}`); code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403 — its mapping says readonly", path, code)
		}
	}
	if code := s.do(t, http.MethodPost, "/graphql", token, gqlMutationBody); code != http.StatusForbidden {
		t.Errorf("GraphQL mutation = %d, want 403", code)
	}
	if n := s.dgraphSaw("mutation"); n != 0 {
		t.Errorf("DGraph received %d mutation(s) from a readonly ServiceAccount", n)
	}

	if code := s.do(t, http.MethodGet, "/api/v1/divergences", token, ""); code != http.StatusOK {
		t.Errorf("GET /api/v1/divergences = %d, want 200 — readonly may read", code)
	}
	if code := s.do(t, http.MethodPost, "/graphql", token, gqlQueryBody); code != http.StatusOK {
		t.Errorf("GraphQL query = %d, want 200 — readonly may read", code)
	}
	if n := s.dgraphSaw("query"); n != 1 {
		t.Errorf("DGraph received %d queries, want 1", n)
	}
}

func TestAppEntry_CallerNeverReadsOrWritesTheUsersTable(t *testing.T) {
	cluster, signSA := newServiceIssuer(t)
	idp, signPerson := newServiceIssuer(t)
	people := auth.ProviderSpec{IssuerURL: idp, Audiences: []string{"armada-orbital"}, UsernameClaim: "email", DefaultRole: "readonly"}
	s := newServiceStack(t, closedClient(t), clusterEntry(cluster), people)

	withEmail := serviceAccountToken(cluster, firmwareSA)
	withEmail["email"] = "firmware@example.com"
	for name, cl := range map[string]map[string]any{
		"ServiceAccount token":    serviceAccountToken(cluster, firmwareSA),
		"token carrying an email": withEmail,
	} {
		if code := s.do(t, http.MethodGet, "/api/v1/divergences", signSA(cl), ""); code != http.StatusOK {
			t.Errorf("%s: got %d, want 200 — a users-table lookup or provision was attempted", name, code)
		}
		if s.lastUserID != nil {
			t.Errorf("%s: user_id = %v, want unset — an app principal is not a user", name, s.lastUserID)
		}
	}

	// Positive control: a person reaches the users table, so the closed
	// database refuses them.
	person := serviceTokenClaims(idp, map[string]any{"sub": "p1", "email": "person@example.com"})
	if code := s.do(t, http.MethodGet, "/api/v1/divergences", signPerson(person), ""); code != http.StatusUnauthorized {
		t.Errorf("person got %d, want 401 from the unreachable users table", code)
	}
}
