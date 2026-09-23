package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
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
	issuerURL   string

	// roleMapper is set when the browser login provider is ALSO configured in
	// ORBITAL_AUTH_PROVIDERS with a roleMapping — i.e. that provider owns roles.
	// Then a browser session gets the same role the same identity would get with
	// a bearer token. Without this the two disagree: one person, one provider,
	// two roles depending on whether they arrived with a cookie or a token.
	// nil means mode A, where orbital's users table owns the role.
	roleMapper *auth.RoleMapper
	// defaultRole is the floor applied when roleMapper matches nothing. Empty
	// means strict: an unmatched login is refused.
	defaultRole string
}

// SetRoleMapper wires group-to-role mapping into the browser login flow. Called
// from server.New when the UI's issuer matches a configured provider.
func (h *OIDC) SetRoleMapper(m *auth.RoleMapper, defaultRole string) {
	h.roleMapper, h.defaultRole = m, defaultRole
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
		issuerURL:   issuerURL,
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

	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(n)

	// PKCE on a CONFIDENTIAL client is deliberate, not belt-and-braces theatre:
	// OAuth 2.1 requires it for all clients. The secret proves which application
	// is redeeming the code; the verifier proves it is the same party that
	// started this flow.
	verifier := oauth2.GenerateVerifier()

	if err := auth.SetOIDCLogin(h.sessionKeys, c.Request(), c.Response(),
		auth.OIDCLogin{State: state, Verifier: verifier, Nonce: nonce}); err != nil {
		return fmt.Errorf("set oidc login: %w", err)
	}

	return c.Redirect(http.StatusFound, h.oauth2Cfg.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier), gooidc.Nonce(nonce)))
}

// Callback handles GET /auth/callback — exchanges the code, verifies the token, creates a session.
func (h *OIDC) Callback(c echo.Context) error {
	login, err := auth.GetAndClearOIDCLogin(h.sessionKeys, c.Request(), c.Response())
	if err != nil || subtle.ConstantTimeCompare([]byte(login.State), []byte(c.QueryParam("state"))) != 1 {
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error="+CodeInvalidState)
	}

	token, err := h.oauth2Cfg.Exchange(c.Request().Context(), c.QueryParam("code"),
		oauth2.VerifierOption(login.Verifier))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error="+CodeNoIDToken)
	}

	idToken, err := h.verifier.Verify(c.Request().Context(), rawIDToken)
	if err != nil {
		return fmt.Errorf("verify id token: %w", err)
	}

	// The nonce binds this ID token to the login attempt that asked for it. A
	// token replayed from another attempt verifies fine — signature, issuer,
	// audience and expiry are all genuine — and only this check catches it.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(login.Nonce)) != 1 {
		h.logger.Warn("oidc callback refused — id token nonce does not match this login attempt",
			"request.id", c.Response().Header().Get(echo.HeaderXRequestID))
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error="+CodeInvalidNonce)
	}

	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return fmt.Errorf("extract claims: %w", err)
	}
	email := strings.ToLower(claims.Email)
	if email == "" {
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error="+CodeIdentityIncomplete)
	}

	displayName := claims.Name
	if displayName == "" {
		displayName = email
	}
	preferredUsername := claims.PreferredUsername
	if preferredUsername == "" {
		preferredUsername = email
	}

	// Provider-owned roles (mode B): resolve before touching the users table, so
	// a caller whose groups match nothing is refused rather than provisioned.
	// Same rule as the bearer path — a mapping enumerates who may use orbital.
	mappedRole, mappedGroup := "", ""
	floored := false
	if h.roleMapper != nil {
		var raw map[string]any
		if err := idToken.Claims(&raw); err != nil {
			return fmt.Errorf("extract claims for role mapping: %w", err)
		}
		// The ID token arrives back-channel, so an operator debugging a role
		// mapping cannot see it in the browser — which makes "no group matched"
		// undiagnosable without this. Logged at DEBUG (ORBITAL_LOG_LEVEL=debug),
		// off by default.
		//
		// CLAIMS ONLY, never the raw token. A raw token is a bearer credential:
		// anyone with the log could replay it. Decoded claims cannot be replayed.
		// GitLab reached the same split for the same reason (gitlab#345435), and
		// Grafana exposes the equivalent via oauth.generic_oauth:debug.
		if h.logger.Enabled(c.Request().Context(), slog.LevelDebug) {
			b, _ := json.Marshal(raw)
			h.logger.Debug("oidc id token claims", "email", email, "claims", string(b))
		}
		role, group, ok := h.roleMapper.RoleFor(raw)
		if !ok && h.defaultRole != "" {
			floored = true
			// Floor: the provider declares a defaultRole alongside its mapping,
			// so a token matching no group lands there instead of being refused.
			// Applies only when the provider already owned this role — an admin's
			// deliberate promotion of someone the mapping never covered is not
			// reverted. A matched group still wins; that is explicit.
			role, group, ok = h.defaultRole, "(no group matched — defaultRole)", true
		}
		if !ok {
			// Name what the token DID carry. "No group matched" is
			// indistinguishable between a missing mapper, an unassigned role and
			// a claim of the wrong shape, and an operator cannot see the token.
			// Claim NAMES only, plus the groups claim's own value — those are
			// role names, not secrets.
			names := make([]string, 0, len(raw))
			for k := range raw {
				names = append(names, k)
			}
			sort.Strings(names)
			h.logger.Warn("oidc login refused — no group matched the provider's role mapping",
				"email", email,
				"groups_claim", h.roleMapper.GroupsClaim(),
				"groups_claim_present", raw[h.roleMapper.GroupsClaim()] != nil,
				"groups_claim_value", fmt.Sprintf("%v", raw[h.roleMapper.GroupsClaim()]),
				"id_token_claims", strings.Join(names, ","))
			return c.Redirect(http.StatusSeeOther, h.basePath+"/?error="+CodeNoRoleMapped)
		}
		mappedRole, mappedGroup = role, group
	}

	ctx := c.Request().Context()
	u, err := h.db.User.Query().Where(user.Email(email)).Only(ctx)
	if err != nil {
		// Provision the user on first login.
		newRole := RoleForEmail(email, h.adminEmails)
		if mappedRole != "" {
			// Groups own the role here, so ORBITAL_ADMIN_EMAILS does not apply —
			// a second way to grant admin outside the mapping defeats the mapping.
			newRole = user.Role(mappedRole)
		}
		create := h.db.User.Create().
			SetEmail(email).
			SetName(displayName).
			SetPreferredUsername(preferredUsername).
			SetVerified(true).
			SetRole(newRole).
			SetIssuer(h.issuerURL)
		if mappedRole != "" {
			create = create.SetRoleSource(user.RoleSourceProvider)
		}
		u, err = create.Save(ctx)
		if err != nil {
			h.logger.Error("provision oidc user", "err", err)
			return fmt.Errorf("provision oidc user: %w", err)
		}
	} else if u.Issuer == nil && u.PasswordHash == nil {
		// Unowned and never a local account — claim it. See authz.go for why
		// both conditions matter.
		if updated, uerr := u.Update().SetIssuer(h.issuerURL).Save(ctx); uerr == nil {
			u = updated
		} else {
			h.logger.Error("could not claim unowned user for provider", "email", email, "err", uerr)
			return fmt.Errorf("claim user: %w", uerr)
		}
	} else if u.Issuer == nil || *u.Issuer != h.issuerURL {
		// A provider may only resolve rows it owns — see authz.go for the
		// reasoning. Refusing here is what keeps a local break-glass account
		// from being claimed by whichever provider asserts its email.
		owner := "a local account"
		if u.Issuer != nil {
			owner = *u.Issuer
		}
		h.logger.Warn("oidc login refused — identity already belongs to another principal",
			"email", email, "token_issuer", h.issuerURL, "row_owner", owner)
		return c.Redirect(http.StatusSeeOther, h.basePath+"/?error="+CodeIdentityConflict)
	} else if mappedRole != "" && string(u.Role) != mappedRole &&
		(!floored || (u.RoleSource != nil && *u.RoleSource == user.RoleSourceProvider)) {
		before := string(u.Role)
		updated, uerr := u.Update().SetRole(user.Role(mappedRole)).SetRoleSource(user.RoleSourceProvider).Save(ctx)
		if uerr != nil {
			h.logger.Error("apply provider role on oidc login", "email", email, "err", uerr)
			return fmt.Errorf("apply provider role: %w", uerr)
		}
		u = updated
		writeAuditEvent(h.db, h.logger, "management", email, "providerRoleChange",
			[]string{"providerRoleChange"}, []string{"User"}, []string{email},
			map[string]any{
				"before": before, "after": mappedRole,
				"provider": h.issuerURL, "group": mappedGroup,
			},
			originFromContext(c, "oidc"),
		)
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
