package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/labstack/echo/v4"
)

// roleLevel defines the linear ordering of roles: readonly < dev < admin.
var roleLevel = map[user.Role]int{
	user.RoleReadonly: 0,
	user.RoleDev:      1,
	user.RoleAdmin:    2,
}

// RoleAtLeast returns true if actual role is >= minimum role in the hierarchy.
func RoleAtLeast(actual, minimum user.Role) bool {
	return roleLevel[actual] >= roleLevel[minimum]
}

// callerRole is the caller's resolved identity for authorization decisions.
// Separated from any single yes/no gate because more than one subsystem needs
// the FACT ("what role is this caller") rather than one gate's verdict: the
// mutation guard asks "is it dev+", and the approval engine asks both
// "is it dev+" and "is it in this policy's bypass_roles".
type callerRole struct {
	// Role is empty only when NoAuthz is true or Reason is set.
	Role user.Role
	// Source is "context" (a role assigned by the provider, e.g. a
	// delegatedAuthorization caller) or "user" (users
	// table). Used for log/error wording.
	Source string
	// NoAuthz means no authz backend is configured at all (nil db) — local dev
	// and unit tests. Deliberately NOT modelled as "admin": treating an absent
	// backend as the highest role would silently hand out bypass privileges.
	NoAuthz bool
	// Reason explains an unresolvable caller.
	Reason string
}

// resolveCallerRole reads the caller's role. Three caller shapes, checked in
// order:
//   - Callers carrying a role on the context (a delegatedAuthorization provider) have a
//     pre-mapped role in context and have NO users-table row, so there is no user_id to
//     look up — honor the context role directly. Mirrors the short-circuit in
//     RequireRole; without it, AEP/Keycloak clients get 403 on every config
//     mutation even though they map to admin.
//   - Session / AAD-bearer callers resolve to a users-table row (user_id) and
//     the role is read from PostgreSQL.
//   - No authz backend (nil db) — see NoAuthz.
func resolveCallerRole(c echo.Context, db *ent.Client) callerRole {
	if cached, ok := c.Get(callerRoleKey).(callerRole); ok {
		return cached
	}
	cr := computeCallerRole(c, db)
	c.Set(callerRoleKey, cr)
	return cr
}

// callerRoleKey memoises the resolved role for the LIFETIME OF ONE REQUEST.
//
// Not a cache with a TTL, and deliberately not one: PostgreSQL stays the
// authority and is re-read on the caller's very next request, so an admin
// changing someone's role takes effect immediately rather than whenever a
// cache expires. What this removes is only the repetition inside a single
// request — RequireRole looks the user up, then the handler asks again, and on
// a /graphql mutation authorizeMutation and writeToDGraph each ask a third and
// fourth time. Same row, same transaction-visible instant, N queries.
const callerRoleKey = "caller_role"

func computeCallerRole(c echo.Context, db *ent.Client) callerRole {
	if roleStr, _ := c.Get("role").(string); roleStr != "" {
		return callerRole{Role: user.Role(roleStr), Source: "context"}
	}
	if db == nil {
		return callerRole{NoAuthz: true}
	}
	userID, _ := c.Get("user_id").(int)
	if userID == 0 {
		// No context role and no resolved user: either auth is disabled
		// (apiAuth empty) or the caller presented no identity. See the
		// "auth: API AUTHENTICATION DISABLED" startup log.
		return callerRole{Reason: "no context role and no authenticated user (auth may be disabled — check startup auth mode)"}
	}
	u, err := db.User.Get(c.Request().Context(), userID)
	if err != nil {
		return callerRole{Reason: "user lookup failed"}
	}
	return callerRole{Role: u.Role, Source: "user"}
}

// RoleForEmail returns admin if email is in adminEmails, otherwise readonly.
func RoleForEmail(email string, adminEmails map[string]struct{}) user.Role {
	if _, ok := adminEmails[email]; ok {
		return user.RoleAdmin
	}
	return user.RoleReadonly
}

// ReconcileAdminEmails promotes every existing user whose email appears in
// adminEmails to admin if their current role is below admin. Called once at
// startup after migrations so that changes to ORBITAL_ADMIN_EMAILS take effect
// on the next deploy — no per-login check required. Users not yet in the DB are
// skipped; RoleForEmail handles the correct role on their first login.
func ReconcileAdminEmails(ctx context.Context, db *ent.Client, adminEmails map[string]struct{}, logger *slog.Logger) {
	if db == nil || len(adminEmails) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	for email := range adminEmails {
		u, err := db.User.Query().Where(user.Email(email)).Only(ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			logger.Warn("ReconcileAdminEmails: query user", "email", email, "err", err)
			continue
		}
		if RoleAtLeast(u.Role, user.RoleAdmin) {
			continue
		}
		prevRole := u.Role
		if _, err := db.User.UpdateOneID(u.ID).SetRole(user.RoleAdmin).Save(ctx); err != nil {
			logger.Warn("ReconcileAdminEmails: promote user", "email", email, "err", err)
			continue
		}
		logger.Info("promoted user to admin via ORBITAL_ADMIN_EMAILS", "email", email, "previous_role", string(prevRole))
		writeAuditEvent(db, logger, "management", "system:adminEmailsConfig", "updateUserRole",
			[]string{"updateUserRole"},
			[]string{},
			[]string{},
			map[string]any{
				"userEmail":    email,
				"previousRole": string(prevRole),
				"newRole":      "admin",
				"reason":       "ORBITAL_ADMIN_EMAILS reconciliation on startup",
			},
			auditInternal(),
		)
	}
}

// ResolveUser is an Echo middleware that resolves a PostgreSQL user ID from the
// email already set on the context by a preceding auth middleware (e.g. bearer
// token verification). It is a no-op when user_id is already set (session auth).
//
// If the email has no matching user row, ResolveUser provisions a new one with
// the role determined by adminEmails — mirroring the OIDC login provisioning path.
// This ensures that service accounts and CI callers using bearer tokens are first-
// class users in the local role table.
func ResolveUser(db *ent.Client, adminEmails map[string]struct{}) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if id, _ := c.Get("user_id").(int); id != 0 {
				return next(c) // session auth already resolved
			}
			// A caller whose role came from its provider (delegatedAuthorization) is
			// intentionally NOT provisioned into the users table — its
			// authorization happened upstream. Skip the lookup/provision so
			// RequireRole's role short-circuit governs it.
			if role, _ := c.Get("role").(string); role != "" {
				return next(c)
			}
			// App-only (client credentials) tokens have no email and no users-table
			// row, by design — the matching provider entry's clientID is the
			// authz gate. Pass through without a DB lookup.
			//
			// Keyed on the marker the verifier set, NOT on the "app:" name
			// prefix: user_name comes from the token's unvalidated `name` claim,
			// so prefix-matching let a human token classify itself as a machine
			// and take this branch, skipping both provisioning and its role.
			if auth.IsAppPrincipal(c) {
				return next(c)
			}
			email, _ := c.Get("user_email").(string)
			email = strings.ToLower(strings.TrimSpace(email))
			if email == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
			}
			ctx := c.Request().Context()
			// Multi-provider role handling (Spike 26). Mode B sets provider_role
			// and is authoritative at every login; mode A sets
			// provider_default_role, which only seeds a NEW user and never
			// overwrites what the table already holds.
			providerRole, _ := c.Get("provider_role").(string)
			providerGroup, _ := c.Get("provider_role_group").(string)
			providerDefault, _ := c.Get("provider_default_role").(string)

			u, err := db.User.Query().Where(user.Email(email)).Only(ctx)
			if err != nil {
				newRole := RoleForEmail(email, adminEmails)
				switch {
				case providerRole != "":
					// Groups own the role for this provider. ORBITAL_ADMIN_EMAILS
					// deliberately does not apply — a second mechanism that can
					// grant admin outside the mapping would defeat the mapping.
					newRole = user.Role(providerRole)
				case providerDefault != "":
					if _, isAdmin := adminEmails[email]; !isAdmin {
						newRole = user.Role(providerDefault)
					}
				}
				create := db.User.Create().
					SetEmail(email).
					SetName(email).
					SetPreferredUsername(email).
					SetVerified(true).
					SetRole(newRole)
				if providerRole != "" {
					create = create.SetRoleSource(user.RoleSourceProvider)
				}
				if iss, _ := c.Get("auth_issuer").(string); iss != "" {
					create = create.SetIssuer(iss)
				}
				u, err = create.Save(ctx)
				if err != nil {
					slog.Default().Warn("ResolveUser: failed to provision user", "email", email, "err", err)
					return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
				}
			} else if iss, _ := c.Get("auth_issuer").(string); iss != "" && u.Issuer == nil && u.PasswordHash == nil {
				// Unowned and never a local account — claim it for this provider.
				// This is the migration path for rows predating the issuer
				// column: without it every existing user is locked out of SSO
				// until an admin backfills.
				//
				// BOTH conditions are load-bearing. A row with a password is a
				// local account and stays unclaimable however old it is, which
				// is what keeps the break-glass admin out of a provider's reach.
				// The branch stops firing permanently once a row has an issuer.
				if updated, uerr := u.Update().SetIssuer(iss).Save(ctx); uerr == nil {
					u = updated
				} else {
					slog.Default().Warn("could not claim unowned user for provider", "email", email, "err", uerr)
					return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
				}
			} else if iss, _ := c.Get("auth_issuer").(string); iss != "" && (u.Issuer == nil || *u.Issuer != iss) {
				// A provider may only resolve rows it owns. It never CLAIMS an
				// existing row — otherwise any configured provider could mint a
				// token for an existing address and inherit that user's role,
				// including a local break-glass admin. Refuse instead.
				//
				// This refusal exists because orbital keys users by EMAIL, which
				// OIDC explicitly says is mutable, reusable and falsifiable. The
				// correct key is (iss, sub) via an identity-link table — how
				// Keycloak, Auth0 and django-social-auth all do it — at which
				// point one person can hold links to several providers and this
				// conflict stops existing. Tracked in docs/planning/backlog.md.
				owner := "a local account"
				if u.Issuer != nil {
					owner = *u.Issuer
				}
				slog.Default().Warn("bearer token rejected — identity already belongs to another principal",
					"email", email, "token_issuer", iss, "row_owner", owner)
				return echo.NewHTTPError(http.StatusUnauthorized, "identity conflict")
			} else if providerRole != "" && string(u.Role) != providerRole && providerRoleApplies(c, u) {
				// The provider owns the role and it moved. Record the transition,
				// not the evaluation: most logins change nothing, and an event
				// per login is noise that trains people to ignore the record.
				before := string(u.Role)
				upd := u.Update().SetRole(user.Role(providerRole)).SetRoleSource(user.RoleSourceProvider)
				if iss, _ := c.Get("auth_issuer").(string); iss != "" {
					upd = upd.SetIssuer(iss)
				}
				updated, uerr := upd.Save(ctx)
				if uerr != nil {
					slog.Default().Warn("ResolveUser: failed to apply provider role", "email", email, "err", uerr)
					return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
				}
				u = updated
				issuer, _ := c.Get("auth_issuer").(string)
				writeAuditEvent(db, slog.Default(), "management", email, "providerRoleChange",
					[]string{"providerRoleChange"}, []string{"User"}, []string{email},
					map[string]any{
						"before": before, "after": providerRole,
						"provider": issuer, "group": providerGroup,
					},
					originFromContext(c, "bearer"),
				)
			}
			c.Set("user_id", u.ID)
			// This is the bearer path — the production one. RequireRole and the
			// handler both want this user's role next; hand them the row already
			// fetched instead of making each re-query it.
			c.Set(callerRoleKey, callerRole{Role: u.Role, Source: "user"})
			return next(c)
		}
	}
}

// RequireRole returns an Echo middleware that enforces the minimum role on mutating
// HTTP methods (POST, PUT, PATCH, DELETE). GET, HEAD, and OPTIONS pass through.
// If db is nil (dev mode without DB), all requests pass through.
// Authorization denials are logged (slog.Warn) and recorded as audit events.
func RequireRole(db *ent.Client, minRole user.Role) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}
			if db == nil {
				return next(c)
			}
			// A delegated-authorization caller carries a pre-mapped role from the
			// middleware, since it is not provisioned into orbital's users
			// table. Trust it directly and skip the DB lookup below.
			if roleStr, _ := c.Get("role").(string); roleStr != "" {
				if RoleAtLeast(user.Role(roleStr), minRole) {
					return next(c)
				}
				slog.Default().Warn("authorization denied",
					"actor", actorFromContext(c),
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"required_role", string(minRole),
					"user_role", roleStr,
					"reason", "context_role_below_required",
				)
				return echo.NewHTTPError(http.StatusForbidden, fmt.Sprintf("role %q is below required %q for this action", roleStr, minRole))
			}
			// App-only (client credentials) callers were authenticated by the
			// provider entry whose clientID matched their azp. MVP policy: any
			// such app caller is treated as `dev`-equivalent — sufficient for
			// all mutating API routes today. Future best practice: check a
			// roles claim against the required role, once the provider can
			// allow defining them. See ADR 010 §App Caller Authorization.
			if auth.IsAppPrincipal(c) {
				if RoleAtLeast(user.RoleDev, minRole) {
					return next(c)
				}
				name, _ := c.Get("user_name").(string)
				slog.Default().Warn("authorization denied",
					"actor", name,
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"required_role", string(minRole),
					"reason", "app_caller_below_required_role",
				)
				return echo.ErrForbidden
			}
			userID, _ := c.Get("user_id").(int)
			if userID == 0 {
				slog.Default().Warn("authorization denied",
					"actor", "",
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"reason", "unauthenticated",
				)
				return echo.NewHTTPError(http.StatusForbidden, "not authenticated — sign in and retry")
			}
			u, err := db.User.Get(c.Request().Context(), userID)
			if err != nil {
				slog.Default().Warn("authorization denied",
					"actor", "",
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"reason", "user_not_found",
				)
				return echo.NewHTTPError(http.StatusForbidden, "user record not found — sign in and retry")
			}
			// The handler downstream will ask for this same role. Seed the memo
			// from the row already in hand rather than making it query again.
			c.Set(callerRoleKey, callerRole{Role: u.Role, Source: "user"})
			if !RoleAtLeast(u.Role, minRole) {
				slog.Default().Warn("authorization denied",
					"actor", u.Email,
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"required_role", string(minRole),
					"user_role", string(u.Role),
				)
				writeAuditEvent(db, slog.Default(), "management", u.Email, "authorizationDenied",
					[]string{"authorizationDenied"},
					[]string{},
					[]string{},
					map[string]any{
						"method":       c.Request().Method,
						"uri":          c.Request().URL.Path,
						"requiredRole": string(minRole),
						"userRole":     string(u.Role),
					},
					originFromContext(c, "rest"),
				)
				return echo.NewHTTPError(http.StatusForbidden, fmt.Sprintf("role %q is below required %q for this action", u.Role, minRole))
			}
			return next(c)
		}
	}
}

// RequireAdmin is a convenience wrapper for RequireRole with admin as the minimum.
// Kept for backwards compatibility with existing tests and call sites.
func RequireAdmin(db *ent.Client) echo.MiddlewareFunc {
	return RequireRole(db, user.RoleAdmin)
}

// providerRoleApplies reports whether a provider-derived role should overwrite
// what the users table holds.
//
// A MATCHED group is explicit and always wins — being added to orbital-admin
// says so regardless of what an admin set before. The FLOOR (defaultRole when
// nothing matched) only applies when the provider already owned this role;
// otherwise it would revert a role an admin deliberately set on someone the
// mapping never covered, which is the case that made "authoritative fallback"
// wrong on its own.
func providerRoleApplies(c echo.Context, u *ent.User) bool {
	floored, _ := c.Get("provider_role_floored").(bool)
	if !floored {
		return true
	}
	return u.RoleSource != nil && *u.RoleSource == user.RoleSourceProvider
}
