package auth

import "github.com/labstack/echo/v4"

func HasRole(c echo.Context, role string) bool {
	roles := RolesFromContext(c)
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

func RolesFromContext(c echo.Context) []string {
	v := c.Get("roles")
	if v == nil {
		return nil
	}
	roles, _ := v.([]string)
	return roles
}
