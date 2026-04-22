# Authentication

> **WIP** — Not yet implemented 

Orbital has three distinct authentication flows depending on the caller:

| Flow | Caller | Mechanism |
|---|---|---|
| 1 | Orbital admin UI | Entra ID OIDC — browser-based login, session cookie |
| 2 | API consumers (e.g. Atlas UI) | JWT bearer token — orbital as resource server, any OIDC-compliant IdP |
| 3 | Orb (edge service) | Long-lived opaque API key — air-gap safe, no external IdP |

---

## Flow 1: Admin UI — Entra ID OIDC

```mermaid
sequenceDiagram
    participant B as Browser
    participant O as Orbital Server
    participant E as Entra ID

    B->>O: GET /admin (no session)
    O-->>B: 302 redirect to Entra ID /authorize
    Note over B,E: ?client_id=&redirect_uri=&scope=openid profile email
    B->>E: Follow redirect — user logs in
    E-->>B: 302 redirect to /auth/callback?code=xxx
    B->>O: GET /auth/callback?code=xxx
    O->>E: POST /token (code + client_secret)
    E-->>O: id_token + access_token
    O->>O: Validate id_token, extract claims
    O-->>B: Set session cookie, redirect to /admin
    B->>O: GET /admin (with session cookie)
    O-->>B: 200 OK — serve UI
```

---

## Flow 2: API Consumer → Orbital API — JWT Bearer

```mermaid
sequenceDiagram
    participant A as API Consumer (e.g. Atlas UI)
    participant O as Orbital Server
    participant K as OIDC Provider JWKS (e.g. Keycloak)

    Note over O: On startup — fetch + cache JWKS
    O->>K: GET /realms/armada/.well-known/openid-configuration
    K-->>O: JWKS public keys (cached, auto-refreshed)

    A->>O: POST /api/topology/query
    Note over A,O: Authorization: Bearer eyJ...
    O->>O: Extract bearer token
    O->>O: Validate signature against cached JWKS
    O->>O: Check iss, exp, azp claims
    O->>O: Extract sub, email, armadaOrgId, groups
    O->>O: Authz — can this user access this data center?
    O-->>A: 200 OK — topology data
```

---

## Flow 3: Orb → Orbital — Long-lived API Key

```mermaid
sequenceDiagram
    participant Admin as Orbital Admin
    participant O as Orbital Server
    participant DB as PostgreSQL
    participant Orb as Orb (edge)

    Admin->>O: Create orb slot for data center
    O->>O: Generate one-time token (1hr TTL)
    O->>DB: Store hashed token + orb metadata
    O-->>Admin: Return plaintext token
    Admin-->>Orb: Deliver token (USB / print / email)

    Note over Orb: Orb startup — needs to register
    Orb->>O: POST /orbs/register {token: "..."}
    O->>DB: Lookup + validate token (not expired, not used)
    O->>DB: Mark token used, create orb record + API key
    O-->>Orb: Return long-lived API key

    Note over Orb: All future requests
    Orb->>O: POST /orbs/poll
    Note over Orb,O: X-Orb-Key: sk_orb_xxxx
    O->>DB: Validate key → resolve orb identity
    O-->>Orb: DGraph export (json.gz + schema.gz)
```
