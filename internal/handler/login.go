package handler

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/web/data/fragment"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

type Login struct {
	db          *ent.Client
	sessionKeys auth.SessionKeys
	formTmpl    *template.Template
}

func NewLogin(db *ent.Client, sessionKeys auth.SessionKeys, formTmpl *template.Template) *Login {
	return &Login{db: db, sessionKeys: sessionKeys, formTmpl: formTmpl}
}

func (h *Login) renderForm(c echo.Context, errMsg string) error {
	csrfToken, _ := c.Get("csrf_token").(string)
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return h.formTmpl.ExecuteTemplate(c.Response().Writer, "login-form.gohtml", fragment.LoginForm{
		CsrfToken: csrfToken,
		ErrorMsg:  errMsg,
	})
}

// Post handles POST /user/login.
func (h *Login) Post(c echo.Context) error {
	email := c.FormValue("email")
	password := c.FormValue("password")
	csrf := c.FormValue("csrf")

	if !auth.ValidateCSRF(h.sessionKeys, c.Request(), csrf) {
		return h.renderForm(c, "Invalid request.")
	}

	u, err := h.db.User.Query().
		Where(user.Email(strings.ToLower(email))).
		Only(c.Request().Context())
	if err != nil {
		return h.renderForm(c, "Invalid email or password.")
	}

	if u.PasswordHash == nil {
		return h.renderForm(c, "This account uses SSO login.")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(password)); err != nil {
		return h.renderForm(c, "Invalid email or password.")
	}

	if err := auth.SetUserSession(h.sessionKeys, c.Request(), c.Response().Writer, u.ID, u.Name, u.Email); err != nil {
		return fmt.Errorf("set session: %w", err)
	}

	c.Response().Header().Set("HX-Redirect", "/?fresh=1")
	return c.NoContent(http.StatusOK)
}

// Logout handles POST /user/logout.
func (h *Login) Logout(c echo.Context) error {
	csrf := c.FormValue("csrf")
	if !auth.ValidateCSRF(h.sessionKeys, c.Request(), csrf) {
		return c.Redirect(http.StatusSeeOther, "/")
	}
	if err := auth.ClearSession(h.sessionKeys, c.Request(), c.Response().Writer); err != nil {
		return fmt.Errorf("clear session: %w", err)
	}
	return c.Redirect(http.StatusSeeOther, "/")
}
