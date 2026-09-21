package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// AuthProviders is the list of identity providers orbital accepts bearer tokens
// from, decoded from ORBITAL_AUTH_PROVIDERS as a JSON array.
//
// A list rather than a single configured provider, because the number is never
// one for long: an adopter arrives with their own IdP and orbital's own is
// already in play. This follows Kubernetes AuthenticationConfiguration, which
// carries up to 64 JWT authenticators each with its own issuer, audiences and
// claim mappings.
//
// JSON in an env var rather than a config file because that is orbital's
// convention — config is envconfig, orbital reads no config file, and a list of
// objects already has a working precedent in ORB_CONSUMERS. ORBITAL_BUNDLER_URLS
// is not a counter-example: it is a hand-rolled `name=url` micro-DSL, and its
// lesson is do not invent a format, not do not put structured data in an env var.
type AuthProviders []AuthProvider

// AuthProvider is one trusted issuer and how orbital treats tokens from it.
type AuthProvider struct {
	Issuer Issuer `json:"issuer"`
	// ClientID is the `azp` this entry matches. Selection is keyed on
	// (issuer, clientID), because one Keycloak realm hosts several clients that
	// orbital must treat differently — a trusted upstream service and a direct
	// caller share an issuer but not a policy. Empty matches any client of that
	// issuer, as an issuer-wide default; a specific entry always wins over it,
	// so selection stays an exact lookup rather than an ordered scan.
	ClientID      string        `json:"clientID,omitempty"`
	ClaimMappings ClaimMappings `json:"claimMappings"`

	// Where this caller's role comes from. DefaultRole alone means orbital's
	// users table owns it and this is what a NEW user is created with.
	// RoleMapping alone means the provider's groups own it, re-derived at every
	// login, and a token matching no group is denied. BOTH TOGETHER IS LEGAL and
	// is the floor: the mapping decides, DefaultRole catches what it does not
	// match, applied authoritatively rather than as a seed (Grafana's
	// role_attribute_path plus role_attribute_strict = false).
	//
	// DelegatedAuthorization is the one that is exclusive — it owns the role
	// wholesale and stores nothing, so combining it with a setting that writes to
	// the users table would be two answers to one question. None of the three is
	// also a startup error: that provider would accept tokens it could do nothing
	// with.
	DefaultRole            string                  `json:"defaultRole,omitempty"`
	RoleMapping            []RoleRule              `json:"roleMapping,omitempty"`
	DelegatedAuthorization *DelegatedAuthorization `json:"delegatedAuthorization,omitempty"`
}

// DelegatedAuthorization marks a caller whose authorization happens upstream.
// Every valid token from it receives Role, and NO user row is provisioned — the
// caller is a service acting for its own users, not an orbital user.
//
// Named for the RFC 8693 §1.1 distinction: this is DELEGATION, not
// impersonation. Both identities survive — the human in `user_email`, the client
// in `acting_client` — so the audit log can say "AEP did X as daniel" rather than
// losing one of them. The adjective is load-bearing: orbital still AUTHENTICATES
// the token itself against the issuer's keys, and only the authorization decision
// is delegated.
//
// Named explicitly so the delegation is visible in config rather than inferred
// from a role that was never overridden.
type DelegatedAuthorization struct {
	Role string `json:"role"`
}

// Issuer identifies the provider and what it is allowed to mint tokens for.
type Issuer struct {
	URL       string   `json:"url"`
	Audiences []string `json:"audiences"`
	// CertificateAuthority is a PEM file path, for a provider behind a private
	// CA. Empty uses the system trust store.
	CertificateAuthority string `json:"certificateAuthority,omitempty"`
}

// ClaimMappings names which claims carry identity and membership. Configurable
// because providers disagree: groups, roles, wids, a namespaced URI.
type ClaimMappings struct {
	Username ClaimRef `json:"username"`
	Groups   ClaimRef `json:"groups,omitempty"`
}

type ClaimRef struct {
	Claim string `json:"claim"`
}

// RoleRule maps one group value to one orbital role.
type RoleRule struct {
	Group string `json:"group"`
	Role  string `json:"role"`
}

// Decode implements envconfig.Decoder for ORBITAL_AUTH_PROVIDERS, matching how
// ORB_CONSUMERS is decoded in orbconfig.
//
// STRICT: an unrecognised field fails startup rather than being ignored.
// encoding/json drops what it does not know, which in an auth config means a
// constraint the operator wrote silently not applying — a field that was renamed
// keeps its old spelling in a manifest and quietly stops doing anything, and
// `client_id` — the OAuth spelling, so the natural way to get `clientID` wrong —
// would leave the entry matching EVERY client of its issuer rather than one.
// (`clientId` is safe: encoding/json matches field names case-insensitively.)
// Kubernetes decodes AuthenticationConfiguration strictly
// for the same reason. Two passes so the message distinguishes malformed JSON
// from a field orbital does not recognise; this runs once, at boot.
func (a *AuthProviders) Decode(value string) error {
	if strings.TrimSpace(value) == "" {
		*a = nil
		return nil
	}
	var parsed AuthProviders
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return fmt.Errorf("ORBITAL_AUTH_PROVIDERS is not valid JSON: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(value))
	dec.DisallowUnknownFields()
	var strict AuthProviders
	if err := dec.Decode(&strict); err != nil {
		return fmt.Errorf("ORBITAL_AUTH_PROVIDERS: %w — `trustedService` is now "+
			"`delegatedAuthorization` (with `role` in place of `assignedRole`), and "+
			"`claimValidationRules` was removed: `clientID` pins azp and "+
			"`issuer.audiences` pins aud", err)
	}
	*a = strict
	return nil
}

var validRoles = map[string]struct{}{"readonly": {}, "dev": {}, "admin": {}}

// Validate rejects a configuration that cannot be enforced as written. Every
// check here fails startup rather than degrading at runtime: an auth config that
// is half-applied is worse than one that refuses to load.
func (a AuthProviders) Validate() error {
	if len(a) == 0 {
		return nil
	}
	seen := make(map[string]int, len(a))
	for i, p := range a {
		where := fmt.Sprintf("provider %d", i)
		if p.Issuer.URL != "" {
			where = fmt.Sprintf("provider %d (%s)", i, p.Issuer.URL)
		}

		if p.Issuer.URL == "" {
			return fmt.Errorf("%s: issuer.url is required", where)
		}
		u, err := url.Parse(p.Issuer.URL)
		if err != nil {
			return fmt.Errorf("%s: issuer.url is not a URL: %w", where, err)
		}
		// https only. The whole trust chain hangs off TLS to the issuer: strip
		// it and anyone who can answer for that hostname becomes the provider.
		if u.Scheme != "https" {
			return fmt.Errorf("%s: issuer.url must be https, got %q", where, u.Scheme)
		}
		// Unique, so a token's `iss` selects exactly one provider. Without this
		// the natural implementation is "try each until one accepts", which lets
		// an attacker pick whichever configured provider is weakest.
		key := p.Issuer.URL + "\x00" + p.ClientID
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("%s: duplicates provider %d — the (issuer.url, clientID) pair must be unique, "+
				"so an incoming token selects exactly one provider", where, prev)
		}
		seen[key] = i

		if len(p.Issuer.Audiences) == 0 {
			return fmt.Errorf("%s: issuer.audiences must list at least one audience", where)
		}
		for _, aud := range p.Issuer.Audiences {
			if strings.TrimSpace(aud) == "" {
				return fmt.Errorf("%s: issuer.audiences contains an empty value", where)
			}
		}
		if p.ClaimMappings.Username.Claim == "" {
			return fmt.Errorf("%s: claimMappings.username.claim is required", where)
		}

		hasDefault, hasMapping := p.DefaultRole != "", len(p.RoleMapping) > 0
		hasDelegated := p.DelegatedAuthorization != nil
		switch {
		case hasDelegated && (hasDefault || hasMapping):
			return fmt.Errorf("%s: delegatedAuthorization cannot be combined with defaultRole or roleMapping — "+
				"it owns the role wholesale and stores nothing", where)
		case !hasDefault && !hasMapping && !hasDelegated:
			return fmt.Errorf("%s: set defaultRole, roleMapping or delegatedAuthorization — "+
				"a provider that cannot assign any role would accept tokens it could do nothing with", where)
		case hasDelegated:
			if _, ok := validRoles[p.DelegatedAuthorization.Role]; !ok {
				return fmt.Errorf("%s: delegatedAuthorization.role must be readonly, dev or admin, got %q",
					where, p.DelegatedAuthorization.Role)
			}
		default:
			// defaultRole and roleMapping may coexist: the mapping decides, and
			// defaultRole is the floor for a token whose groups match nothing.
			// Mapping alone denies instead — the same pair Grafana exposes as
			// role_attribute_path plus role_attribute_strict.
			if hasDefault {
				if _, ok := validRoles[p.DefaultRole]; !ok {
					return fmt.Errorf("%s: defaultRole must be readonly, dev or admin, got %q", where, p.DefaultRole)
				}
			}
			if !hasMapping {
				break
			}
			// Groups decide the role here, so orbital has to know which claim
			// carries them.
			if p.ClaimMappings.Groups.Claim == "" {
				return fmt.Errorf("%s: roleMapping requires claimMappings.groups.claim, "+
					"so orbital knows which claim carries membership", where)
			}
			for j, r := range p.RoleMapping {
				if r.Group == "" {
					return fmt.Errorf("%s: roleMapping[%d].group is required", where, j)
				}
				if _, ok := validRoles[r.Role]; !ok {
					return fmt.Errorf("%s: roleMapping[%d].role must be readonly, dev or admin, got %q", where, j, r.Role)
				}
			}
		}
	}
	return nil
}
