package configitems

import "strings"

// Slug derivation: a type name becomes the URL segment its pages live under.
//
// DERIVED FROM THE TYPE NAME, never from stored orbIds. The orbId convention
// (`<namespace>:<kind>-<natural-key>`) is not universal — legacy types predate
// it, so IPAddress ids read `<ns>:<address>` and Rack ids read `<ns>:<rackName>`
// with no kind token at all. Deriving a slug from stored ids would work for
// every type that has caught up and fail on exactly the ones that have not.
// kebab(TypeName) is the same transformation the orbId convention applies to
// kinds, so the two stay in lockstep while the data catches up.
//
// A slug is a CONTRACT, not a label. It appears in URLs that integrators and
// bookmarks depend on, so it derives from the schema and changing it is a
// deliberate breaking act — the same reason Kubernetes puts `plural` in the CRD
// spec rather than in anyone's preferences. The human-readable nav label is a
// separate, display-only concern.

// KebabTypeName converts a GraphQL type name to kebab-case.
//
// Acronyms are handled by looking at both neighbours: a capital starts a new
// word when the previous character is lower-case or a digit, or when it is
// followed by a lower-case letter while itself following a capital. That second
// rule is what makes `IPAddress` split as ip/address rather than i/p/address,
// and `S3Sync` split as s3/sync.
func KebabTypeName(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' && i > 0 {
			prev := name[i-1]
			var next byte
			if i+1 < len(name) {
				next = name[i+1]
			}
			prevLower := prev >= 'a' && prev <= 'z'
			prevDigit := prev >= '0' && prev <= '9'
			prevUpper := prev >= 'A' && prev <= 'Z'
			nextLower := next >= 'a' && next <= 'z'
			if prevLower || prevDigit || (prevUpper && nextLower) {
				b.WriteByte('-')
			}
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Pluralize applies English plural rules good enough for type names.
//
// The interesting case is a name that is ALREADY plural: `IdracSettings`
// kebabs to "idrac-settings", and a naive +s yields "idrac-settingss". A name
// ending in a single "s" is treated as already plural and left alone, while one
// ending in a double "s" ("ip-address") takes -es. That distinction is the whole
// reason this is not one line.
func Pluralize(s string) string {
	switch {
	case s == "":
		return s
	case strings.HasSuffix(s, "ss"), strings.HasSuffix(s, "x"), strings.HasSuffix(s, "z"),
		strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"):
		return s + "es"
	case strings.HasSuffix(s, "s"):
		return s // already plural
	case strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(s[len(s)-2]):
		return s[:len(s)-1] + "ies"
	default:
		return s + "s"
	}
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

// DerivedSlug is the default slug for a type.
func DerivedSlug(typeName string) string {
	return Pluralize(KebabTypeName(typeName))
}

// SlugAnnotation is the docstring form that overrides the derived slug:
//
//	"""slug: clusters"""
//	type EksaKubernetesCluster implements ... {
//
// It lives in the SCHEMA, deliberately, and not in the runtime override layer.
// A slug is a contract: if it could be changed from a preferences UI, every
// integrator's URLs would move underneath them. In the schema it is
// version-controlled, reviewed alongside the type, and changing it is a visible
// breaking act — which is exactly how Kubernetes treats `plural`.
const SlugAnnotation = "slug:"

// SlugFor returns a type's slug, preferring an annotation over derivation.
func SlugFor(typeName, typeDoc string) string {
	if v := annotationValue(typeDoc, SlugAnnotation); v != "" {
		return v
	}
	return DerivedSlug(typeName)
}
