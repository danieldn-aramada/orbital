package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v4"
)

// ExternalJWTVerifier validates bearer tokens issued by an external OIDC
// provider (e.g. AEP's Keycloak `aep-fleet-commander` client) and populates the
// echo.Context with actor identity + a pre-mapped role.
//
// The middleware:
//   - Accepts a Bearer token OR (as a fallback) an authenticated session
//     cookie. The bearer path is for API callers proxied by an upstream
//     service (e.g. AEP's backend); the session fallback lets orbital's own UI
//     keep working — humans sign in via local/OIDC login and browse with a
//     session cookie, since a browser can't attach a bearer to a plain page
//     navigation. Session auth relies on the global session middleware having
//     already set is_authn=true.
//   - Enforces an explicit `azp` (authorized party) equality check on bearer
//     tokens. When the audience is a generic default like Keycloak's built-in
//     `account`, the `azp` claim is what bounds trust to a specific client.
//     Without it, any token signed by the realm would validate. See RFC 9068 §4.
//   - Assigns a single fixed role from configuration to bearer callers; it does
//     NOT read roles from the JWT. AEP enforces authz upstream; orbital treats
//     every valid token as authorized at the configured tier.
//
// For bearer callers the middleware sets `role` on the context so RequireRole
// can grant without a PostgreSQL user lookup — external users are not
// provisioned into orbital's users table. Session callers set no `role`; their
// role is resolved from the DB by RequireRole via the session's user_id.
type ExternalJWTVerifier struct {
	verifier    *gooidc.IDTokenVerifier
	issuer      string
	clientID    string
	defaultRole string

	// fallback verifies bearers whose `iss` is NOT this verifier's issuer —
	// e.g. the in-pod cb-bundler's AAD client-credentials service token. This
	// keeps internal service-to-service auth working: external-jwt ADDS
	// Keycloak-user acceptance, it does not replace the AAD service path.
	// nil means no fallback (bearers from other issuers are rejected).
	fallback *BearerVerifier
}

// ExternalJWTConfig captures the runtime parameters wired from Config.
type ExternalJWTConfig struct {
	IssuerURL   string
	Audience    string
	ClientID    string          // required `azp` claim value (RFC 9068 client_id)
	DefaultRole string          // "readonly" | "dev" | "admin"
	Fallback    *BearerVerifier // optional: verifies bearers from other issuers (AAD service tokens)
}

func NewExternalJWTVerifier(ctx context.Context, cfg ExternalJWTConfig) (*ExternalJWTVerifier, error) {
	provider, err := gooidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("external-jwt provider discovery: %w", err)
	}
	return &ExternalJWTVerifier{
		verifier:    provider.Verifier(&gooidc.Config{ClientID: cfg.Audience}),
		issuer:      cfg.IssuerURL,
		clientID:    cfg.ClientID,
		defaultRole: cfg.DefaultRole,
		fallback:    cfg.Fallback,
	}, nil
}

// RequireAuth is an Echo middleware that accepts either a valid Bearer token or
// an authenticated session cookie. Returns 401 with an RFC 6749 §5.2 /
// RFC 6750 §3.1 error body (`{error, error_description}`) and a matching
// WWW-Authenticate header when neither is present or valid.
func (v *ExternalJWTVerifier) RequireAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if rawToken, ok := strings.CutPrefix(c.Request().Header.Get("Authorization"), "Bearer "); ok {
				// Route by (unverified) issuer. Bearers from a different issuer
				// than this verifier's — e.g. the in-pod bundler's AAD service
				// token — go to the fallback verifier so internal service auth
				// keeps working. Keycloak-issued bearers are verified here. The
				// iss peek is unverified but only selects WHICH verifier runs;
				// the chosen verifier still fully validates the signature.
				if v.fallback != nil {
					if claims, err := parseUnverifiedClaims(rawToken); err == nil && claims.Iss != "" && claims.Iss != v.issuer {
						return v.fallback.verifyBearer(c, next, rawToken)
					}
				}
				return v.verifyBearer(c, next, rawToken)
			}
			// No bearer — fall back to the session cookie set by the global
			// session middleware. This keeps orbital's own UI usable: a human
			// who signed in via local/OIDC login browses with a session, while
			// AEP's API calls carry a bearer. Session role comes from the DB
			// (RequireRole looks it up by user_id), NOT from the JWT default.
			if isAuthn, _ := c.Get("is_authn").(bool); isAuthn {
				return next(c)
			}
			return v.deny(c, oauthErrInvalidRequest, "authentication required: present a Bearer token or sign in")
		}
	}
}

func (v *ExternalJWTVerifier) verifyBearer(c echo.Context, next echo.HandlerFunc, rawToken string) error {
	idToken, err := v.verifier.Verify(c.Request().Context(), rawToken)
	if err != nil {
		v.logVerifyFailure(c, rawToken, err)
		return v.deny(c, oauthErrInvalidToken, err.Error())
	}

	var claims externalJWTClaims
	if err := idToken.Claims(&claims); err != nil {
		return v.deny(c, oauthErrInvalidToken, "unable to parse token claims: "+err.Error())
	}

	if claims.AZP != v.clientID {
		// Verify() succeeded, so the signature is valid and these claims are
		// authentic — safe to attribute the identity the token was minted for.
		slog.Warn("external-jwt rejected — azp mismatch",
			"request.id", requestID(c),
			"azp", claims.AZP,
			"expected_azp", v.clientID,
			"subject", claims.Sub,
			"user_email", claims.actorEmail(),
			"client.address", c.RealIP(),
		)
		return v.deny(c, oauthErrInvalidToken, fmt.Sprintf("token azp %q is not an authorized client for this API", claims.AZP))
	}

	c.Set("user_name", claims.Name)
	c.Set("user_email", claims.actorEmail())
	c.Set("user_sub", claims.Sub)
	c.Set("role", v.defaultRole)
	c.Set("is_authn", true)

	return next(c)
}

// logVerifyFailure emits a WARN for a bearer that failed go-oidc verification.
//
// It attributes an identity ONLY when the signature was cryptographically
// valid — i.e. an expired token. go-oidc checks expiry AFTER signature, issuer,
// and audience, so a TokenExpiredError means the trusted issuer really minted
// this token for this subject; the claims are authentic and safe to log. This
// matches the auth-log convention of Okta/Auth0/CloudTrail.
//
// For any other failure (bad signature, wrong issuer/audience, malformed) the
// claims are attacker-controllable, so we log the reason + source only and
// deliberately do NOT attribute an identity — logging unverified claims as
// identity is both misleading and a log-forging vector.
func (v *ExternalJWTVerifier) logVerifyFailure(c echo.Context, rawToken string, err error) {
	var expired *gooidc.TokenExpiredError
	if errors.As(err, &expired) {
		claims, _ := parseUnverifiedClaims(rawToken) // signature already validated before the expiry check
		slog.Warn("external-jwt rejected — token expired",
			"request.id", requestID(c),
			"err", err.Error(),
			"subject", claims.Sub,
			"user_email", claims.actorEmail(),
			"client.address", c.RealIP(),
		)
		return
	}
	slog.Warn("external-jwt verification failed",
		"request.id", requestID(c),
		"err", err.Error(),
		"client.address", c.RealIP(),
	)
}

// parseUnverifiedClaims decodes the JWT payload segment WITHOUT signature
// verification. Only for logging an already-signature-validated (but expired)
// token's authentic identity — never for authorization decisions.
func parseUnverifiedClaims(rawToken string) (externalJWTClaims, error) {
	var claims externalJWTClaims
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
// correlating this auth-failure log with the access-log line for the same
// request.
func requestID(c echo.Context) string {
	return c.Response().Header().Get(echo.HeaderXRequestID)
}

// OAuth 2.0 error codes for Bearer token failures (RFC 6750 §3.1).
const (
	oauthErrInvalidRequest = "invalid_request"
	oauthErrInvalidToken   = "invalid_token"
)

// deny writes the RFC 6749 §5.2 / RFC 6750 §3.1 error response: a matching
// WWW-Authenticate header for HTTP intermediaries, and a JSON body carrying the
// error code + human-readable description for API clients.
func (v *ExternalJWTVerifier) deny(c echo.Context, code, description string) error {
	c.Response().Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error=%q, error_description=%q`, code, description))
	return c.JSON(http.StatusUnauthorized, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

type externalJWTClaims struct {
	Iss               string `json:"iss"`
	Sub               string `json:"sub"`
	AZP               string `json:"azp"`
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

// actorEmail returns the email-preferred human identifier, falling back to
// preferred_username. This mirrors actorFromContext (the canonical identity
// helper) so a user is attributed identically in the success path (user_email
// on context), in audit records, and in auth-failure logs — an operator
// grepping an email finds all three. `sub` is logged alongside as the stable,
// authoritative anchor; this is the human-readable convenience field.
func (c externalJWTClaims) actorEmail() string {
	if c.Email != "" {
		return c.Email
	}
	return c.PreferredUsername
}
