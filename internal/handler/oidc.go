package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
)

type OIDC struct {
	db          *ent.Client
	sessionKeys auth.SessionKeys
	oauth2Cfg   oauth2.Config
	verifier    *gooidc.IDTokenVerifier
	logger      *slog.Logger
	basePath    string
	adminEmails map[string]struct{}
}

func NewOIDC(ctx context.Context, db *ent.Client, sessionKeys auth.SessionKeys, issuerURL, clientID, clientSecret, redirectURL, basePath string, logger *slog.Logger, adminEmails map[string]struct{}) (*OIDC, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc provider discovery: %w", err)
	}

	if logger == nil {
		logger = slog.Default()
	}
	h := &OIDC{
		db:          db,
		sessionKeys: sessionKeys,
		oauth2Cfg: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{gooidc.ScopeOpenID, "email", "profile"},
		},
		verifier:    provider.Verifier(&gooidc.Config{ClientID: clientID}),
		logger:      logger,
		basePath:    basePath,
		adminEmails: adminEmails,
	}
	return h, nil
}

// Login handles GET /auth/login — redirects to the IdP.
func (h *OIDC) Login(c echo.Context) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generate state: %w", err)
	}
	state := base64.URLEncoding.EncodeToString(b)

	if err := auth.SetOIDCState(h.sessionKeys, c.Request(), c.Response(), state); err != nil {
		return fmt.Errorf("set oidc state: %w", err)
	}

	return c.Redirect(http.StatusFound, h.oauth2Cfg.AuthCodeURL(state))
}

// Callback handles GET /auth/callback — exchanges the code, verifies the token, creates a session.
func (h *OIDC) Callback(c echo.Context) error {
	storedState, err := auth.GetAndClearOIDCState(h.sessionKeys, c.Request(), c.Response())
	if err != nil || storedState != c.QueryParam("state") {
		return c.Redirect(http.StatusSeeOther, "/?error=invalid_state")
	}

	token, err := h.oauth2Cfg.Exchange(c.Request().Context(), c.QueryParam("code"))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return c.Redirect(http.StatusSeeOther, "/?error=no_id_token")
	}

	idToken, err := h.verifier.Verify(c.Request().Context(), rawIDToken)
	if err != nil {
		return fmt.Errorf("verify id token: %w", err)
	}

	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return fmt.Errorf("extract claims: %w", err)
	}
	h.logger.Info("oidc callback claims", "email", claims.Email, "name", claims.Name, "preferred_username", claims.PreferredUsername)

	email := strings.ToLower(claims.Email)
	if email == "" {
		return c.Redirect(http.StatusSeeOther, "/?error=no_email")
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
		// Provision the user on first login.
		u, err = h.db.User.Create().
			SetEmail(email).
			SetName(displayName).
			SetPreferredUsername(preferredUsername).
			SetVerified(true).
			SetRole(RoleForEmail(email, h.adminEmails)).
			Save(c.Request().Context())
		if err != nil {
			h.logger.Error("provision oidc user", "err", err)
			return fmt.Errorf("provision oidc user: %w", err)
		}
	}
	if err := auth.SetUserSession(h.sessionKeys, c.Request(), c.Response(), u.ID, u.Name, u.Email, string(u.Role)); err != nil {
		return fmt.Errorf("set session: %w", err)
	}

	ua := c.Request().UserAgent()
	h.writeAuthAudit(c, "loginSuccess", email, map[string]any{"method": "oidc", "user_agent": ua})
	return c.Redirect(http.StatusSeeOther, h.basePath+"/?fresh=1")
}

// writeAuthAudit persists an authentication audit event. No-op if db is nil.
func (h *OIDC) writeAuthAudit(c echo.Context, operation, actor string, details map[string]any) {
	if h.db == nil {
		return
	}
	writeAuditEvent(h.db, h.logger, "auth", actor, operation,
		[]string{operation},
		[]string{},
		[]string{},
		details,
		originFromContext(c, "rest"),
	)
}
