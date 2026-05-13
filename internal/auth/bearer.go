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

type BearerVerifier struct {
	verifier *gooidc.IDTokenVerifier
}

func NewBearerVerifier(ctx context.Context, issuerURL, audience string) (*BearerVerifier, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc provider discovery: %w", err)
	}
	return &BearerVerifier{
		verifier: provider.Verifier(&gooidc.Config{ClientID: audience}),
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
}
