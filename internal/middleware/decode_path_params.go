package middleware

import (
	"net/url"

	"github.com/labstack/echo/v4"
)

// DecodePathParams URL-decodes every Echo path parameter before the handler
// runs. Registered globally so handlers never have to call url.PathUnescape
// themselves.
//
// Echo doesn't auto-decode path params (see github.com/labstack/echo/issues/1582),
// which silently breaks any handler whose param value can contain a reserved URL
// char. In this codebase that's orbIds: e.g. "colo:colo-galleon" — JS encodes
// the ":" as "%3A", Echo would otherwise pass the literal "colo%3Acolo-galleon"
// to the handler, and the downstream DGraph lookup wouldn't match.
//
// Decoding is a no-op for params whose values are already URL-safe (DGraph UIDs,
// UUIDs, integers, OCI tags) — only the byte sequence changes when reserved
// chars are present. Decoding errors fall through to the raw value.
func DecodePathParams(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		names := c.ParamNames()
		if len(names) == 0 {
			return next(c)
		}
		values := make([]string, len(names))
		for i, n := range names {
			v := c.Param(n)
			if decoded, err := url.PathUnescape(v); err == nil {
				values[i] = decoded
			} else {
				values[i] = v
			}
		}
		c.SetParamValues(values...)
		return next(c)
	}
}
