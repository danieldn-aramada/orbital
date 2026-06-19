package handler

import "strings"

// SafeDomID converts an orbId (which may contain ":") into a CSS-selector-safe
// DOM identifier. Non-alphanumeric chars become "_". The JS-side equivalent is
// `safeDomId()` in web/shared/static/shared.js — both must use the same mapping
// so server-rendered IDs match JS-built selectors.
//
// Templates emit {{.DomID}} for HTML id attributes; {{.OrbID}} for URL paths
// and data attributes that carry the canonical resource identity.
func SafeDomID(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
