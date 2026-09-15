package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v4"
)

// OrgSvcOIDC implements orbital's Keycloak browser login routed through
// armada-organization-svc instead of orbital holding its own Keycloak client
// credentials. Active when ORBITAL_OAUTH2_DEVICE_CODE=false — see oidc.go
// for the ORBITAL_OAUTH2_DEVICE_CODE=true (Microsoft/EntraID device-code)
// counterpart; server.go registers exactly one of the two per deployment.
// See docs/reference/AUTH.md § Keycloak web login (org-svc) for the full
// contract.
type OrgSvcOIDC struct {
	db          *ent.Client
	sessionKeys auth.SessionKeys
	orgSvcURL   string
	redirectURL string
	httpClient  *http.Client
	verifier    *gooidc.IDTokenVerifier
	logger      *slog.Logger
	basePath    string
	adminEmails map[string]struct{}
}

func NewOrgSvcOIDC(ctx context.Context, db *ent.Client, sessionKeys auth.SessionKeys, issuerURL, orgSvcURL, redirectURL, basePath string, logger *slog.Logger, adminEmails map[string]struct{}) (*OrgSvcOIDC, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc provider discovery: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OrgSvcOIDC{
		db:          db,
		sessionKeys: sessionKeys,
		orgSvcURL:   strings.TrimSuffix(orgSvcURL, "/"),
		redirectURL: redirectURL,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		// SkipClientIDCheck: orbital holds no Keycloak client id of its own in
		// this flow — armada-organization-svc's shared client does the actual
		// exchange with Keycloak. Orbital verifies the returned token's
		// signature and issuer only; there is no audience of orbital's own to
		// check it against. See docs/reference/AUTH.md § Keycloak web login
		// (org-svc).
		verifier:    provider.Verifier(&gooidc.Config{SkipClientIDCheck: true}),
		logger:      logger,
		basePath:    basePath,
		adminEmails: adminEmails,
	}, nil
}

// orgSvcLoginRequest is the body sent to POST /api/v1/login/sso.
type orgSvcLoginRequest struct {
	Email       string `json:"email"`
	RedirectURL string `json:"redirectUrl"`
}

// orgSvcLoginResponse is the shape returned by POST /api/v1/login/sso.
type orgSvcLoginResponse struct {
	RedirectURL string `json:"redirectUrl"`
}

// Login handles GET /auth/login?email=... — asks armada-organization-svc for
// the Keycloak redirect URL and sends the browser there. Unlike the direct
// Keycloak flow, this requires the user's email up front: org-svc resolves
// which organization/IdP to use from the email's domain.
func (h *OrgSvcOIDC) Login(c echo.Context) error {
	email := strings.ToLower(strings.TrimSpace(c.QueryParam("email")))
	if email == "" {
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=email_required")
	}

	reqBody, err := json.Marshal(orgSvcLoginRequest{Email: email, RedirectURL: h.redirectURL})
	if err != nil {
		return fmt.Errorf("marshal org-svc login request: %w", err)
	}

	resp, err := h.httpClient.Post(h.orgSvcURL+"/api/v1/login/sso", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		h.logger.Error("organization-svc login/sso request failed", "err", err)
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=sso_unavailable")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		h.logger.Warn("organization-svc login/sso rejected", "status", resp.StatusCode, "body", string(body))
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=sso_rejected")
	}

	var out orgSvcLoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode org-svc login/sso response: %w", err)
	}
	if out.RedirectURL == "" {
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=sso_rejected")
	}

	return c.Redirect(http.StatusFound, out.RedirectURL)
}

// orgSvcTokenResponse is the shape returned by GET /api/v1/login/token/sso.
type orgSvcTokenResponse struct {
	UserID         string `json:"userId"`
	OrganizationID string `json:"organizationId"`
	Token          struct {
		AccessToken string `json:"access_token"`
	} `json:"token"`
}

// Callback handles GET /auth/callback — receives Keycloak's redirect (via
// armada-organization-svc's registered client), exchanges the code through
// org-svc, verifies the resulting access token, and sets orbital's session.
func (h *OrgSvcOIDC) Callback(c echo.Context) error {
	code := c.QueryParam("code")
	if code == "" {
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=no_code")
	}

	// org-svc appends organization/referrer to the redirectUrl we originally
	// gave it before handing it to Keycloak, and Keycloak echoes those back
	// verbatim on this callback — org-svc's token endpoint needs them again to
	// reconstruct the exact same redirect_uri it used during the code grant.
	q := url.Values{"code": {code}, "redirectUrl": {h.redirectURL}}
	if organization := c.QueryParam("organization"); organization != "" {
		q.Set("organization", organization)
	}
	if referrer := c.QueryParam("referrer"); referrer != "" {
		q.Set("referrer", referrer)
	}

	resp, err := h.httpClient.Get(h.orgSvcURL + "/api/v1/login/token/sso?" + q.Encode())
	if err != nil {
		h.logger.Error("organization-svc login/token/sso request failed", "err", err)
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=sso_unavailable")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		h.logger.Warn("organization-svc login/token/sso rejected", "status", resp.StatusCode, "body", string(body))
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=sso_rejected")
	}

	var out orgSvcTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode org-svc login/token/sso response: %w", err)
	}
	if out.Token.AccessToken == "" {
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=no_token")
	}

	idToken, err := h.verifier.Verify(c.Request().Context(), out.Token.AccessToken)
	if err != nil {
		h.logger.Warn("org-svc access token verification failed", "err", err)
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=invalid_token")
	}

	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return fmt.Errorf("extract claims: %w", err)
	}
	h.logger.Info("org-svc oidc callback claims", "email", claims.Email, "name", claims.Name, "preferred_username", claims.PreferredUsername)

	email := strings.ToLower(claims.Email)
	if email == "" {
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error=no_email")
	}

	displayName := claims.Name
	if displayName == "" {
		displayName = email
	}
	preferredUsername := claims.PreferredUsername
	if preferredUsername == "" {
		preferredUsername = email
	}

	u, err := h.db.User.Query().Where(user.Email(email)).Only(c.Request().Context())
	if err != nil {
		u, err = h.db.User.Create().
			SetEmail(email).
			SetName(displayName).
			SetPreferredUsername(preferredUsername).
			SetVerified(true).
			SetRole(RoleForEmail(email, h.adminEmails)).
			Save(c.Request().Context())
		if err != nil {
			h.logger.Error("provision org-svc oidc user", "err", err)
			return fmt.Errorf("provision org-svc oidc user: %w", err)
		}
	}
	if err := auth.SetUserSession(h.sessionKeys, c.Request(), c.Response(), u.ID, u.Name, u.Email, string(u.Role)); err != nil {
		return fmt.Errorf("set session: %w", err)
	}

	ua := c.Request().UserAgent()
	h.writeAuthAudit("loginSuccess", email, map[string]any{"method": "org-svc-oidc", "user_agent": ua})
	return c.Redirect(http.StatusSeeOther, h.basePath+"/?fresh=1")
}

// writeAuthAudit persists an authentication audit event. No-op if db is nil.
func (h *OrgSvcOIDC) writeAuthAudit(operation, actor string, details map[string]any) {
	if h.db == nil {
		return
	}
	writeAuditEvent(h.db, h.logger, "auth", actor, operation,
		[]string{operation},
		[]string{},
		[]string{},
		details,
	)
}
