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
	ClientID             string                `json:"clientID,omitempty"`
	ClaimValidationRules []ClaimValidationRule `json:"claimValidationRules,omitempty"`
	ClaimMappings        ClaimMappings         `json:"claimMappings"`

	// Exactly one of DefaultRole or RoleMapping. DefaultRole means orbital's
	// users table owns the role and this is what a NEW user is created with.
	// RoleMapping means the provider's groups own the role, re-derived at every
	// login. Both or neither is a startup error — a setting that is present and
	// inoperative is how someone comes to believe it is doing something it is not.
	DefaultRole    string          `json:"defaultRole,omitempty"`
	RoleMapping    []RoleRule      `json:"roleMapping,omitempty"`
	TrustedService *TrustedService `json:"trustedService,omitempty"`
}

// TrustedService marks a caller whose authorization happens upstream. Every
// valid token from it receives AssignedRole, and NO user row is provisioned —
// the caller is a service acting for its own users, not an orbital user.
//
// This is a deliberate trust delegation, not a default that happens to apply to
// everyone: the upstream service authenticates and authorizes, and orbital
// accepts its judgement. Named explicitly so that is visible in config rather
// than inferred from a role that was never overridden.
type TrustedService struct {
	AssignedRole string `json:"assignedRole"`
}

// Issuer identifies the provider and what it is allowed to mint tokens for.
type Issuer struct {
	URL       string   `json:"url"`
	Audiences []string `json:"audiences"`
	// CertificateAuthority is a PEM file path, for a provider behind a private
	// CA. Empty uses the system trust store.
	CertificateAuthority string `json:"certificateAuthority,omitempty"`
}

// ClaimValidationRule requires a claim to hold an exact value. This is how azp
// anchoring is expressed: Keycloak defaults to `aud: account` for every client
// in a realm, so an audience check there proves only "some client in this realm"
// and `azp` is the claim that actually identifies the caller.
type ClaimValidationRule struct {
	Claim         string `json:"claim"`
	RequiredValue string `json:"requiredValue"`
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
func (a *AuthProviders) Decode(value string) error {
	if strings.TrimSpace(value) == "" {
		*a = nil
		return nil
	}
	var parsed AuthProviders
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return fmt.Errorf("ORBITAL_AUTH_PROVIDERS is not valid JSON: %w", err)
	}
	*a = parsed
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
		for j, r := range p.ClaimValidationRules {
			if r.Claim == "" || r.RequiredValue == "" {
				return fmt.Errorf("%s: claimValidationRules[%d] needs both claim and requiredValue", where, j)
			}
		}
		if p.ClaimMappings.Username.Claim == "" {
			return fmt.Errorf("%s: claimMappings.username.claim is required", where)
		}

		hasDefault, hasMapping := p.DefaultRole != "", len(p.RoleMapping) > 0
		hasTrusted := p.TrustedService != nil
		switch {
		case hasTrusted && (hasDefault || hasMapping):
			return fmt.Errorf("%s: trustedService cannot be combined with defaultRole or roleMapping — "+
				"it owns the role wholesale and stores nothing", where)
		case !hasDefault && !hasMapping && !hasTrusted:
			return fmt.Errorf("%s: set defaultRole, roleMapping or trustedService — "+
				"a provider that cannot assign any role would accept tokens it could do nothing with", where)
		case hasTrusted:
			if _, ok := validRoles[p.TrustedService.AssignedRole]; !ok {
				return fmt.Errorf("%s: trustedService.assignedRole must be readonly, dev or admin, got %q",
					where, p.TrustedService.AssignedRole)
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
