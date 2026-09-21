package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v4"
)

// ProviderSet verifies bearer tokens against a LIST of trusted identity
// providers, selecting one by the token's `iss`.
//
// Selection is a lookup, never a loop. Issuer URLs are unique (enforced in
// config validation), so exactly one provider ever attempts cryptographic
// validation of a given token. The alternative — try each verifier until one
// accepts — lets an attacker aim at whichever configured provider is weakest,
// which is why Kubernetes makes issuer URLs unique in AuthenticationConfiguration
// rather than leaving ordering to the operator.
//
// The `iss` peek is unverified. It only selects WHICH provider runs; the chosen
// provider still validates the signature against its own published keys, so a
// token claiming provider A's issuer but signed by provider B is rejected.
type ProviderSet struct {
	// byKey is keyed "issuer\x00clientID". A token selects its provider by the
	// (iss, azp) pair, falling back to the issuer-wide entry with an empty
	// clientID. Two exact lookups, never an ordered scan — one Keycloak realm
	// hosts several clients orbital must treat differently, and "try each until
	// one accepts" would let a caller aim at whichever is most permissive.
	byKey  map[string]*provider
	logger *slog.Logger
}

func providerKey(issuer, clientID string) string { return issuer + "\x00" + clientID }

type provider struct {
	issuer        string
	clientID      string
	delegatedRole string
	verifier      *gooidc.IDTokenVerifier
	audiences     []string
	selfClientID  string
	usernameClaim string
	groupsClaim   string
	defaultRole   string
	mapper        *RoleMapper
}

// roleGroupDefaulted marks an audit event whose role came from the provider's
// defaultRole floor rather than a matched group, so the record says which.
const roleGroupDefaulted = "(no group matched — defaultRole)"

type roleRule struct{ group, role string }

// ProviderSpec is the runtime shape of one configured provider, mapped from
// config.AuthProvider so this package does not import config.
type ProviderSpec struct {
	IssuerURL string
	// ClientID is the azp this entry matches; empty means any client of the issuer.
	ClientID string
	// DelegatedRole is set for a provider with delegatedAuthorization: every
	// valid token gets this role and no user row is provisioned.
	DelegatedRole        string
	Audiences            []string
	CertificateAuthority string
	// SelfClientID is the client orbital's own UI and CLI authenticate as. A
	// token from it has no separate actor.
	SelfClientID  string
	UsernameClaim string
	GroupsClaim   string
	DefaultRole   string
	RoleMapping   []struct{ Group, Role string }
}

// NewProviderSet builds verifiers for every spec. Discovery failure for one
// provider does NOT fail the whole set: orbital must serve what does not need
// that provider, and an IdP outage should not prevent boot. The failure is
// logged and that provider's tokens are refused until a restart picks it up.
func NewProviderSet(ctx context.Context, specs []ProviderSpec, logger *slog.Logger) (*ProviderSet, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("no auth providers configured")
	}
	ps := &ProviderSet{byKey: make(map[string]*provider, len(specs)), logger: logger}
	for _, s := range specs {
		pctx := ctx
		if s.CertificateAuthority != "" {
			cl, err := httpClientWithCA(s.CertificateAuthority)
			if err != nil {
				return nil, fmt.Errorf("provider %s: %w", s.IssuerURL, err)
			}
			pctx = gooidc.ClientContext(ctx, cl)
		}
		disc, err := gooidc.NewProvider(pctx, s.IssuerURL)
		if err != nil {
			logger.Error("auth provider discovery failed — tokens from this issuer will be refused until orbital restarts",
				"issuer", s.IssuerURL, "err", err)
			continue
		}
		p := &provider{
			issuer:        s.IssuerURL,
			clientID:      s.ClientID,
			delegatedRole: s.DelegatedRole,
			selfClientID:  s.SelfClientID,
			usernameClaim: s.UsernameClaim,
			groupsClaim:   s.GroupsClaim,
			defaultRole:   s.DefaultRole,
		}
		// go-oidc's ClientID check handles exactly one audience, but the config
		// allows several and the token must match ANY of them. So skip its check
		// and do the membership test here — one code path for both cases, rather
		// than two that could diverge.
		p.verifier = disc.Verifier(&gooidc.Config{SkipClientIDCheck: true})
		p.audiences = append(p.audiences, s.Audiences...)
		p.mapper = NewRoleMapper(s.GroupsClaim, s.RoleMapping)
		ps.byKey[providerKey(s.IssuerURL, s.ClientID)] = p
	}
	return ps, nil
}

func httpClientWithCA(path string) (*http.Client, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read certificateAuthority: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("certificateAuthority %s contains no usable PEM certificate", path)
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}, nil
}

// Issuers lists configured (issuer, clientID) pairs, for startup logging.
func (ps *ProviderSet) Issuers() []string {
	out := make([]string, 0, len(ps.byKey))
	for k := range ps.byKey {
		out = append(out, strings.Replace(k, "\x00", " client=", 1))
	}
	return out
}

func bearerFrom(c echo.Context) (string, bool) {
	return strings.CutPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
}

// RequireAuth accepts a valid Bearer token from any configured provider, or an
// authenticated session cookie. The bearer path is the API; the session path
// keeps orbital's own UI working, where the role comes from the users table
// rather than from a token.
func (ps *ProviderSet) RequireAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			raw, ok := bearerFrom(c)
			if !ok {
				if isAuthn, _ := c.Get("is_authn").(bool); isAuthn {
					return next(c)
				}
				return denyBearer(c, oauthErrInvalidRequest, "authentication required: present a Bearer token or sign in")
			}
			return ps.verify(c, next, raw)
		}
	}
}

func (ps *ProviderSet) verify(c echo.Context, next echo.HandlerFunc, raw string) error {
	// Peek at `iss` to select the provider. Unverified, and it only decides
	// WHICH provider validates — the chosen one still checks the signature
	// against its own keys, so a token claiming another provider's issuer is
	// rejected by that provider rather than accepted by this lookup.
	peek, err := parseUnverifiedClaims(raw)
	if err != nil {
		return denyBearer(c, oauthErrInvalidToken, "malformed bearer token")
	}
	// Select by (iss, azp), then fall back to the issuer-wide entry. Both are
	// exact lookups; the specific entry always wins, so a caller cannot steer
	// itself toward a more permissive provider.
	azp := peek.AZP
	p := ps.byKey[providerKey(peek.Iss, azp)]
	if p == nil {
		p = ps.byKey[providerKey(peek.Iss, "")]
	}
	if p == nil {
		// Do not echo the issuer back: an unauthenticated caller learns nothing
		// about what is configured. The operator gets it in the log.
		ps.logger.Warn("bearer token rejected — no configured provider for this issuer and client",
			"issuer", peek.Iss, "client", azp, "request.id", requestID(c))
		return denyBearer(c, oauthErrInvalidToken, "token issuer is not trusted by this server")
	}

	idToken, err := p.verifier.Verify(c.Request().Context(), raw)
	if err != nil {
		ps.logger.Warn("bearer token verification failed",
			"issuer", p.issuer, "err", err, "request.id", requestID(c))
		return denyBearer(c, oauthErrInvalidToken, "token verification failed")
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return denyBearer(c, oauthErrInvalidToken, "token claims are unreadable")
	}
	if !audienceMatches(claims["aud"], p.audiences) {
		ps.logger.Warn("bearer token rejected — audience mismatch",
			"issuer", p.issuer, "want", p.audiences, "request.id", requestID(c))
		return denyBearer(c, oauthErrInvalidToken, "token audience is not accepted by this server")
	}
	c.Set("auth_issuer", p.issuer)
	c.Set("is_authn", true)

	// App principal: a client-credentials caller with no human behind it — the
	// in-pod cb-bundler is the worked example. Valid, but not a user: it must
	// not land in the users table with a role. Detected as "carries a client
	// identity and no email" (email == "" && appID != ""), the test the removed
	// single-issuer verifier applied, generalised across providers.
	//
	// This is not hypothetical. Keycloak creates a real user object for a
	// service-enabled client, so `preferred_username` is populated while `email`
	// is absent — a provider mapping username to preferred_username would
	// otherwise provision `service-account-<client>` as a readonly user, which
	// is what happened the first time this was run against the dev realm.
	appID := claimString(claims["azp"])
	if appID == "" {
		appID = claimString(claims["appid"])
	}
	if claimString(claims["email"]) == "" && appID != "" {
		MarkAppPrincipal(c)
		c.Set("user_name", AppPrincipalPrefix+appID)
		c.Set("user_email", "")
		return next(c)
	}

	username := claimString(claims[p.usernameClaim])
	if username == "" {
		ps.logger.Warn("bearer token rejected — username claim is absent",
			"issuer", p.issuer, "claim", p.usernameClaim, "request.id", requestID(c))
		return denyBearer(c, oauthErrInvalidToken, "token carries no usable identity")
	}
	c.Set("user_name", claimString(claims["name"]))
	c.Set("user_email", username)

	// When a trusted upstream service presents a token carrying a human subject,
	// the client is the ACTOR and the human is the SUBJECT. Record the actor so
	// the audit log can distinguish "daniel did X" from "AEP did X as daniel".
	// RFC 8693 carries this in `act`; until orbital consumes exchanged tokens it
	// is inferred from the verified azp. Skipped when the client is orbital's
	// own — there the user is their own actor and stamping it is noise.
	if appID != "" && appID != p.selfClientID {
		c.Set("acting_client", appID)
	}

	// Delegated authorization: the decision happened upstream, so every valid
	// token gets the configured role and NO user row is provisioned. The caller is
	// a service acting for its own users, not an orbital user — same treatment as
	// an app principal, and the same reason.
	if p.delegatedRole != "" {
		c.Set("role", p.delegatedRole)
		return next(c)
	}

	// Role. Mode B (roleMapping) derives it from groups at every login and is
	// authoritative; mode A (defaultRole) only seeds a NEW user and never
	// overwrites what the users table already holds. ResolveUser applies these.
	if p.mapper != nil {
		role, group, ok := p.mapper.RoleFor(claims)
		if !ok {
			if p.defaultRole == "" {
				// Strict: a mapping enumerates who may use orbital, and someone
				// in no mapped group was not enumerated. Grafana's
				// role_attribute_strict = true.
				ps.logger.Warn("bearer token rejected — no group matched this provider's role mapping",
					"issuer", p.issuer, "groups_claim", p.groupsClaim, "request.id", requestID(c))
				return denyBearer(c, oauthErrInvalidToken, "no group in this token maps to a role on this server")
			}
			// Floor: defaultRole alongside a mapping means "map, else this".
			// Applied AUTHORITATIVELY, not as a seed — otherwise a user removed
			// from their group would keep the role the mapping gave them, and
			// revocation would silently fail.
			role, group = p.defaultRole, roleGroupDefaulted
			c.Set("provider_role_floored", true)
		}
		c.Set("provider_role", role)
		c.Set("provider_role_group", group)
	} else {
		c.Set("provider_default_role", p.defaultRole)
	}
	return next(c)
}

// rolePrecedence orders orbital's roles so the most privileged match wins when
// several groups map. Grafana resolves competing mappings the same way.
var rolePrecedence = map[string]int{"readonly": 1, "dev": 2, "admin": 3}

// RoleMapper resolves an orbital role from a token's group claim for one
// provider.
//
// Exported and shared deliberately: both the bearer path (ProviderSet) and the
// browser login path (handler.OIDC) must reach the same answer for the same
// identity, and two copies of this logic is exactly how they would drift into
// giving one person different roles depending on whether they arrived with a
// token or a cookie.
type RoleMapper struct {
	groupsClaim string
	rules       []roleRule
}

// NewRoleMapper builds a mapper. Returns nil when there are no rules, so callers
// can use a nil mapper to mean "this provider does not own roles" (mode A).
func NewRoleMapper(groupsClaim string, rules []struct{ Group, Role string }) *RoleMapper {
	if len(rules) == 0 {
		return nil
	}
	m := &RoleMapper{groupsClaim: groupsClaim}
	for _, r := range rules {
		m.rules = append(m.rules, roleRule{group: r.Group, role: r.Role})
	}
	return m
}

// GroupsClaim names the claim this mapper reads.
func (m *RoleMapper) GroupsClaim() string { return m.groupsClaim }

// RoleFor returns the most privileged role any of the token's groups maps to.
// ok is false when nothing matches, which callers must treat as "denied" rather
// than falling back to a default — a mapping enumerates who may use orbital.
func (m *RoleMapper) RoleFor(claims map[string]any) (role, group string, ok bool) {
	present := map[string]struct{}{}
	switch v := claims[m.groupsClaim].(type) {
	case string:
		present[v] = struct{}{}
	case []any:
		for _, x := range v {
			if s, sok := x.(string); sok {
				present[s] = struct{}{}
			}
		}
	}
	best := 0
	for _, r := range m.rules {
		if _, in := present[r.group]; !in {
			continue
		}
		if rank := rolePrecedence[r.role]; rank > best {
			best, role, group, ok = rank, r.role, r.group, true
		}
	}
	return role, group, ok
}

// audienceMatches reports whether any accepted audience appears in the token's
// `aud`, which JWT allows to be either a string or an array of strings.
func audienceMatches(aud any, accepted []string) bool {
	present := map[string]struct{}{}
	switch v := aud.(type) {
	case string:
		present[v] = struct{}{}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				present[s] = struct{}{}
			}
		}
	}
	for _, a := range accepted {
		if _, ok := present[a]; ok {
			return true
		}
	}
	return false
}

func claimString(v any) string {
	s, _ := v.(string)
	return s
}

// denyBearer writes the RFC 6750 §3.1 error response: a WWW-Authenticate header
// for HTTP intermediaries and a JSON body carrying the code and description.
func denyBearer(c echo.Context, code, description string) error {
	c.Response().Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error=%q, error_description=%q`, code, description))
	return c.JSON(http.StatusUnauthorized, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

// unverifiedClaims is the minimum needed to CHOOSE a provider: the issuer and
// the authorized party. Decoded without signature verification, so it is only
// ever used for selection — the chosen provider validates the token against its
// own keys before any claim here is trusted.
type unverifiedClaims struct {
	Iss string `json:"iss"`
	AZP string `json:"azp"`
}

// parseUnverifiedClaims decodes a JWT payload WITHOUT verifying the signature.
func parseUnverifiedClaims(rawToken string) (unverifiedClaims, error) {
	var claims unverifiedClaims
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("malformed jwt: got %d segments, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, fmt.Errorf("decode jwt payload: %w", err)
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, fmt.Errorf("unmarshal jwt claims: %w", err)
	}
	return claims, nil
}

// requestID returns the Echo request ID set by the RequestID middleware, for
// correlating an auth-failure log with the access-log line for the same request.
func requestID(c echo.Context) string {
	return c.Response().Header().Get(echo.HeaderXRequestID)
}

// OAuth 2.0 error codes for Bearer token failures (RFC 6750 §3.1).
const (
	oauthErrInvalidRequest = "invalid_request"
	oauthErrInvalidToken   = "invalid_token"
)
