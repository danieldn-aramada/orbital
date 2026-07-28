package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/labstack/echo/v4"
)

// UsersHandler handles user management API endpoints.
type UsersHandler struct {
	db     *ent.Client
	logger *slog.Logger
}

func NewUsersHandler(db *ent.Client, logger *slog.Logger) *UsersHandler {
	return &UsersHandler{db: db, logger: logger}
}

type userItem struct {
	ID                int    `json:"id"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferredUsername"`
	Role              string `json:"role"`
	CreatedAt         string `json:"createdAt"`
	Note              string `json:"note,omitempty"`
}

// List handles GET /api/v1/users — admin only.
//
// @Summary     List users
// @Description Returns all users in the orbital user table. Admin only.
// @Tags        users
// @Produce     json
// @Success     200 {object} map[string]any
// @Failure     403 {object} errorResponse
// @Router      /api/v1/users [get]
func (h *UsersHandler) List(c echo.Context) error {
	if err := h.enforceAdmin(c); err != nil {
		return err
	}

	users, err := h.db.User.Query().Order(user.ByCreatedAt(sql.OrderAsc())).All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("query users: %w", err)
	}

	items := make([]userItem, len(users))
	for i, u := range users {
		items[i] = userItem{
			ID:                u.ID,
			Email:             u.Email,
			Name:              u.Name,
			PreferredUsername: u.PreferredUsername,
			Role:              string(u.Role),
			CreatedAt:         u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"users": items})
}

type updateRoleRequest struct {
	Role string `json:"role"`
}

// UpdateRole handles PUT /api/v1/users/:id/role — admin only.
// Accepts role values: "readonly", "dev", "admin".
// Returns 409 if the change would leave zero admin users.
//
// @Summary     Update user role
// @Description Changes a user's role. Accepts `readonly`, `dev`, or `admin`. Admin only. Returns 409 if changing the role would leave zero admins (last-admin guard).
// @Tags        users
// @Accept      json
// @Produce     json
// @Param       id   path  int               true "User ID"
// @Param       body body  updateRoleRequest true "New role"
// @Success     200  {object} userItem
// @Failure     400  {object} errorResponse
// @Failure     403  {object} errorResponse
// @Failure     404  {object} errorResponse
// @Failure     409  {object} errorResponse
// @Router      /api/v1/users/{id}/role [put]
func (h *UsersHandler) UpdateRole(c echo.Context) error {
	// Validate parameters first so callers get 400 before any auth/DB check.
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid user id")
	}

	var req updateRoleRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	var newRole user.Role
	switch req.Role {
	case "readonly":
		newRole = user.RoleReadonly
	case "dev":
		newRole = user.RoleDev
	case "admin":
		newRole = user.RoleAdmin
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "role must be readonly, dev, or admin")
	}

	if err := h.enforceAdmin(c); err != nil {
		return err
	}

	ctx := c.Request().Context()

	target, err := h.db.User.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		}
		return fmt.Errorf("get user: %w", err)
	}

	// Idempotent: already the requested role.
	if target.Role == newRole {
		return c.JSON(http.StatusOK, toUserItem(target))
	}

	// Last-admin guard: reject if this would remove the last admin.
	if target.Role == user.RoleAdmin && newRole != user.RoleAdmin {
		adminCount, err := h.db.User.Query().Where(user.RoleEQ(user.RoleAdmin)).Count(ctx)
		if err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if adminCount <= 1 {
			return echo.NewHTTPError(http.StatusConflict, "cannot change the last admin user's role")
		}
	}

	updated, err := h.db.User.UpdateOneID(id).SetRole(newRole).Save(ctx)
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}

	actor := actorFromContext(c)
	writeAuditEvent(h.db, h.logger, "management", actor, "updateUserRole",
		[]string{"updateUserRole"},
		[]string{"User"},
		[]string{updated.Email},
		map[string]any{
			"targetUserId": updated.ID,
			"targetEmail":  updated.Email,
			"oldRole":      string(target.Role),
			"newRole":      string(newRole),
		},
	)

	item := toUserItem(updated)
	item.Note = "User must re-login for UI role change to take effect"
	return c.JSON(http.StatusOK, item)
}

func toUserItem(u *ent.User) userItem {
	return userItem{
		ID:                u.ID,
		Email:             u.Email,
		Name:              u.Name,
		PreferredUsername: u.PreferredUsername,
		Role:              string(u.Role),
		CreatedAt:         u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// enforceAdmin checks that the request comes from an admin user.
// Returns 403 if not admin, 503 if db is unavailable.
func (h *UsersHandler) enforceAdmin(c echo.Context) error {
	if h.db == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "database not available")
	}
	userID, _ := c.Get("user_id").(int)
	if userID == 0 {
		return echo.ErrForbidden
	}
	u, err := h.db.User.Get(c.Request().Context(), userID)
	if err != nil || !RoleAtLeast(u.Role, user.RoleAdmin) {
		return echo.ErrForbidden
	}
	return nil
}
