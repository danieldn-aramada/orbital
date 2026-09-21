package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v4"
)

// AppPrincipalPrefix tags user-context identities synthesized from app-only
// (machine-to-machine) bearer tokens. Used by audit-log actor extraction to
// distinguish service callers from human users. See ADR 010.
const AppPrincipalPrefix = "app:"

type BearerVerifier struct {
	verifier *gooidc.IDTokenVerifier

	// allowedAppIDs restricts which appid claims are accepted for app-only
	// tokens. EMPTY DENIES every app token — omission is never permissive in an
	// authorization allowlist. allowAnyAppID is the explicit opt-out, spelled
	// "*". See docs/reference/AUTH.md § App Caller Authorization.
	allowedAppIDs map[string]struct{}
	allowAnyAppID bool
}

// AppIDWildcard accepts any app-only token whose signature, issuer and audience
// already verified, regardless of which application minted it.
//
// It is spelled as a value rather than inferred from an empty list because
// "I did not configure this" and "I intend to allow everything" must not be the
// same input. That equivalence is a documented, exploited failure mode: AWS IAM
// trust policies that omitted the `sub` condition accepted tokens from ANY
// GitHub Actions workflow, and AWS now refuses to create such a policy at all.
// Vault spells its wildcard "*" for the same reason; an Istio AuthorizationPolicy
// with no rules denies rather than allows.
const AppIDWildcard = "*"

// NewBearerVerifier creates a verifier that validates OIDC bearer tokens
// against the given issuer's JWKS. The audience string is the expected `aud`
// claim — for Microsoft Entra v2.0 tokens this is the application's client ID
// (the bare GUID). allowedAppIDs gates which app-only tokens are accepted: nil
// or empty rejects every app token, AppIDWildcard ("*") accepts any, and a list
// accepts exactly those application ids.
func NewBearerVerifier(ctx context.Context, issuerURL, audience string, allowedAppIDs []string) (*BearerVerifier, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc provider discovery: %w", err)
	}
	allowed := make(map[string]struct{}, len(allowedAppIDs))
	anyApp := false
	for _, id := range allowedAppIDs {
		id = strings.TrimSpace(id)
		switch id {
		case "":
		case AppIDWildcard:
			anyApp = true
		default:
			allowed[id] = struct{}{}
		}
	}
	return &BearerVerifier{
		verifier:      provider.Verifier(&gooidc.Config{ClientID: audience}),
		allowedAppIDs: allowed,
		allowAnyAppID: anyApp,
	}, nil
}

// RequireAuth is an Echo middleware that accepts either a valid Bearer token or
// an authenticated session cookie. Bearer tokens are verified via OIDC and
// populate user context from JWT claims. Session auth relies on the global
// session middleware having already set is_authn=true on the context.
// Returns 401 if neither is present or valid.
func (v *BearerVerifier) RequireAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if rawToken, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
				return v.verifyBearer(c, next, rawToken)
			}
			if isAuthn, _ := c.Get("is_authn").(bool); isAuthn {
				return next(c)
			}
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
	}
}

func (v *BearerVerifier) verifyBearer(c echo.Context, next echo.HandlerFunc, rawToken string) error {
	idToken, err := v.verifier.Verify(c.Request().Context(), rawToken)
	if err != nil {
		slog.Warn("bearer token verification failed", "err", err)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var claims azureClaims
	if err := idToken.Claims(&claims); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	email := claims.PreferredUsername
	if email == "" {
		email = claims.UPN
	}

	// App-only (client credentials) tokens carry no user identity — appid/azp
	// is present, email/upn are empty. Synthesize an actor string for audit
	// attribution and gate on the allowlist if configured. See ADR 010.
	if appID := claims.effectiveAppID(); email == "" && appID != "" {
		if !v.appIDAllowed(appID) {
			slog.Warn("bearer token rejected — appid not in allowlist",
				"appid", appID, "allowlist_size", len(v.allowedAppIDs))
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
		c.Set("user_name", AppPrincipalPrefix+appID)
		c.Set("user_email", "")
		c.Set("roles", claims.Roles)
		c.Set("is_authn", true)
		return next(c)
	}

	c.Set("user_name", claims.Name)
	c.Set("user_email", email)
	c.Set("roles", claims.Roles)
	c.Set("is_authn", true)

	return next(c)
}

type azureClaims struct {
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	UPN               string   `json:"upn"`
	Roles             []string `json:"roles"`
	AppID             string   `json:"appid"` // present on app-only tokens (v1.0); v2.0 uses `azp`
	AZP               string   `json:"azp"`   // authorized party (v2.0 equivalent of appid)
}

// effectiveAppID returns the caller's app identity for v1.0 or v2.0 tokens.
// Microsoft v1.0 issues `appid`; v2.0 issues `azp` (and may also include appid).
func (c azureClaims) effectiveAppID() string {
	if c.AppID != "" {
		return c.AppID
	}
	return c.AZP
}

// appIDAllowed reports whether an app-only token from appID may proceed. An
// unconfigured allowlist returns false for every appID — that is the point.
func (v *BearerVerifier) appIDAllowed(appID string) bool {
	if v.allowAnyAppID {
		return true
	}
	_, ok := v.allowedAppIDs[appID]
	return ok
}
