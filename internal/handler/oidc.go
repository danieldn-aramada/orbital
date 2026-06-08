package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	webtemplates "github.com/armada/orbital/web/templates/orbital"
	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
)

type OIDC struct {
	db                *ent.Client
	sessionKeys       auth.SessionKeys
	oauth2Cfg         oauth2.Config
	verifier          *gooidc.IDTokenVerifier
	logger            *slog.Logger
	basePath          string
	adminEmails       map[string]struct{}
	deviceCodeEnabled bool
	deviceCodeURL     string // POST endpoint to initiate device code flow
	deviceCodeTmpl    *template.Template
}

func NewOIDC(ctx context.Context, db *ent.Client, sessionKeys auth.SessionKeys, issuerURL, clientID, clientSecret, redirectURL, basePath string, logger *slog.Logger, adminEmails map[string]struct{}, deviceCodeEnabled bool) (*OIDC, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc provider discovery: %w", err)
	}

	// Derive the device code endpoint from the issuer URL.
	// Azure AD: https://login.microsoftonline.com/{tenant}/v2.0
	//        →  https://login.microsoftonline.com/{tenant}/oauth2/v2.0/devicecode
	deviceCodeURL := strings.TrimSuffix(issuerURL, "/v2.0") + "/oauth2/v2.0/devicecode"

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
		verifier:          provider.Verifier(&gooidc.Config{ClientID: clientID}),
		logger:            logger,
		basePath:          basePath,
		adminEmails:       adminEmails,
		deviceCodeEnabled: deviceCodeEnabled,
		deviceCodeURL:     deviceCodeURL,
	}
	if deviceCodeEnabled {
		h.deviceCodeTmpl = webtemplates.DeviceCodePage()
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

	if err := auth.SetOIDCState(h.sessionKeys, c.Request(), c.Response().Writer, state); err != nil {
		return fmt.Errorf("set oidc state: %w", err)
	}

	return c.Redirect(http.StatusFound, h.oauth2Cfg.AuthCodeURL(state))
}

// Callback handles GET /auth/callback — exchanges the code, verifies the token, creates a session.
func (h *OIDC) Callback(c echo.Context) error {
	storedState, err := auth.GetAndClearOIDCState(h.sessionKeys, c.Request(), c.Response().Writer)
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

	if err := auth.SetUserSession(h.sessionKeys, c.Request(), c.Response().Writer, u.ID, u.Name, u.Email, string(u.Role)); err != nil {
		return fmt.Errorf("set session: %w", err)
	}

	h.writeAuthAudit("loginSuccess", email, map[string]any{"method": "oidc"})
	return c.Redirect(http.StatusSeeOther, h.basePath+"/?fresh=1")
}

// deviceCodePageData is the template data for the device code page.
type deviceCodePageData struct {
	BasePath        string
	UserCode        string
	VerificationURI string
	DeviceCode      string
	Interval        int
}

// DeviceCodeStart handles GET /auth/device — requests a device code from Azure AD
// and renders the page showing the user_code and verification URI.
func (h *OIDC) DeviceCodeStart(c echo.Context) error {
	resp, err := http.PostForm(h.deviceCodeURL, url.Values{
		"client_id": {h.oauth2Cfg.ClientID},
		"scope":     {strings.Join(h.oauth2Cfg.Scopes, " ")},
	})
	if err != nil {
		return fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var dc struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
		ErrorDesc       string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &dc); err != nil {
		return fmt.Errorf("decode device code response: %w", err)
	}
	if dc.Error != "" {
		return fmt.Errorf("device code error: %s: %s", dc.Error, dc.ErrorDesc)
	}
	if dc.Interval == 0 {
		dc.Interval = 5 // Azure AD default
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return h.deviceCodeTmpl.ExecuteTemplate(c.Response().Writer, "device-code-page.gohtml", deviceCodePageData{
		BasePath:        h.basePath,
		UserCode:        dc.UserCode,
		VerificationURI: dc.VerificationURI,
		DeviceCode:      dc.DeviceCode,
		Interval:        dc.Interval,
	})
}

// devicePollResponse is the JSON shape returned by DeviceCodePoll.
type devicePollResponse struct {
	Status   string `json:"status"`             // "pending", "complete", "expired"
	Interval int    `json:"interval,omitempty"` // set when Azure returns slow_down
}

// DeviceCodePoll handles POST /auth/device/poll — polls the Azure AD token endpoint
// once and returns the current status as JSON. On success it sets the session cookie
// so the next page load is authenticated.
// device_code is sent in the POST body (not as a query param) to keep it out of
// application logs, proxy access logs, and browser history.
func (h *OIDC) DeviceCodePoll(c echo.Context) error {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := c.Bind(&req); err != nil || req.DeviceCode == "" {
		return c.JSON(http.StatusBadRequest, devicePollResponse{Status: "expired"})
	}
	deviceCode := req.DeviceCode

	resp, err := http.PostForm(h.oauth2Cfg.Endpoint.TokenURL, url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {h.oauth2Cfg.ClientID},
		"device_code": {deviceCode},
	})
	if err != nil {
		return fmt.Errorf("device code poll: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
		Interval    int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode poll response: %w", err)
	}

	switch result.Error {
	case "":
		// Success — verify the id_token, provision user, set session.
	case "authorization_pending":
		return c.JSON(http.StatusOK, devicePollResponse{Status: "pending"})
	case "slow_down":
		interval := result.Interval
		if interval == 0 {
			interval = 10
		}
		return c.JSON(http.StatusOK, devicePollResponse{Status: "pending", Interval: interval})
	default:
		h.logger.Info("device code auth failed", "error", result.Error)
		h.writeAuthAudit("loginFailed", "", map[string]any{"method": "device_code", "error": result.Error})
		return c.JSON(http.StatusOK, devicePollResponse{Status: "expired"})
	}

	idToken, err := h.verifier.Verify(c.Request().Context(), result.IDToken)
	if err != nil {
		return fmt.Errorf("verify device code id token: %w", err)
	}

	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return fmt.Errorf("extract device code claims: %w", err)
	}

	email := strings.ToLower(claims.Email)
	if email == "" {
		return c.JSON(http.StatusOK, devicePollResponse{Status: "expired"})
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
			h.logger.Error("provision device code user", "err", err)
			return fmt.Errorf("provision device code user: %w", err)
		}
	}

	if err := auth.SetUserSession(h.sessionKeys, c.Request(), c.Response().Writer, u.ID, u.Name, u.Email, string(u.Role)); err != nil {
		return fmt.Errorf("set device code session: %w", err)
	}

	h.logger.Info("device code auth success", "email", email)
	h.writeAuthAudit("loginSuccess", email, map[string]any{"method": "device_code"})
	return c.JSON(http.StatusOK, devicePollResponse{Status: "complete"})
}

// writeAuthAudit persists an authentication audit event. No-op if db is nil.
func (h *OIDC) writeAuthAudit(operation, actor string, details map[string]any) {
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
