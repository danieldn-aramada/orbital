//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// Acceptance item 6: the audit log names an app principal as `app:<client_id>`,
// read back through GET /api/v1/audit-log rather than the table. The negative is
// a person reaching orbital through a client on the same issuer: the `app:`
// label must not attach to them, and `actingClient` must not attach to the app
// principal, whose client is the actor rather than acting for someone.
func TestAuditLog_NamesTheServiceCaller(t *testing.T) {
	db := testutil.NewTestDB(t)
	iss, sign := newServiceIssuer(t)
	viaClient := auth.ProviderSpec{
		IssuerURL: iss, ClientID: "aep-fleet-commander", Audiences: []string{"armada-orbital"},
		SelfClientID: "armada-orbital", UsernameClaim: "email", DelegatedRole: "readonly",
	}
	s := newServiceStack(t, db, firmwareClientSpec(iss), viaClient)

	const person = "operator@auditapp.test"
	app := sign(clientCredentialsToken(iss, "armada-firmware-svc"))
	human := sign(serviceTokenClaims(iss, map[string]any{
		"sub": "p1", "email": person, "azp": "aep-fleet-commander", "client_id": "aep-fleet-commander",
	}))
	for name, token := range map[string]string{"app principal": app, "person via client": human} {
		if code := s.do(t, http.MethodPost, "/api/v1/backup", token, `{}`); code != http.StatusForbidden {
			t.Fatalf("%s: POST /api/v1/backup = %d, want 403 so a refusal is recorded", name, code)
		}
	}

	h := NewEventHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-log?operation=authorizationDenied&limit=500", nil)
	rec := httptest.NewRecorder()
	if err := h.List(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audit-log = %d: %s", rec.Code, rec.Body.String())
	}
	var resp auditLogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byActor := map[string][]eventItem{}
	for _, ev := range resp.Events {
		byActor[ev.Actor] = append(byActor[ev.Actor], ev)
	}

	appEvents := byActor["app:armada-firmware-svc"]
	if len(appEvents) != 1 {
		t.Fatalf("events attributed to app:armada-firmware-svc = %d, want 1; actors seen: %v", len(appEvents), slices.Collect(maps.Keys(byActor)))
	}
	if ac := appEvents[0].ActingClient; ac != "" {
		t.Errorf("app principal event carries actingClient %q, want none — the client is the actor", ac)
	}

	personEvents := byActor[person]
	if len(personEvents) != 1 {
		t.Fatalf("events attributed to %s = %d, want 1; actors seen: %v", person, len(personEvents), slices.Collect(maps.Keys(byActor)))
	}
	if ac := personEvents[0].ActingClient; ac != "aep-fleet-commander" {
		t.Errorf("person event actingClient = %q, want aep-fleet-commander", ac)
	}
	if n := len(byActor["app:aep-fleet-commander"]); n != 0 {
		t.Errorf("%d event(s) attributed to app:aep-fleet-commander — a person's token was labelled as an app principal", n)
	}
}

// Declared app entries, read back through the consumer APIs. Names transcribed
// from the declared-app-principal acceptance list (items 2 and 5).

func TestAppEntry_CallerNeverAppearsOnTheUsersPage(t *testing.T) {
	db := testutil.NewTestDB(t)
	cluster, signSA := newServiceIssuer(t)
	idp, signPerson := newServiceIssuer(t)
	people := auth.ProviderSpec{IssuerURL: idp, Audiences: []string{"armada-orbital"}, UsernameClaim: "email", DefaultRole: "readonly"}
	s := newServiceStack(t, db, clusterEntry(cluster), people)

	withEmail := serviceAccountToken(cluster, firmwareSA)
	withEmail["email"] = "firmware@userspage.test"
	for name, cl := range map[string]map[string]any{
		"ServiceAccount token":    serviceAccountToken(cluster, firmwareSA),
		"token carrying an email": withEmail,
	} {
		token := signSA(cl)
		if code := s.do(t, http.MethodGet, "/api/v1/divergences", token, ""); code != http.StatusOK {
			t.Fatalf("%s: GET = %d, want 200", name, code)
		}
		if code := s.do(t, http.MethodPost, "/api/v1/backup", token, `{}`); code != http.StatusForbidden {
			t.Fatalf("%s: POST = %d, want 403", name, code)
		}
	}
	// Positive control: a person on the same chain is provisioned, so the page
	// does show rows this chain creates.
	const person = "person@userspage.test"
	if code := s.do(t, http.MethodGet, "/api/v1/divergences",
		signPerson(serviceTokenClaims(idp, map[string]any{"sub": "p1", "email": person})), ""); code != http.StatusOK {
		t.Fatalf("person: GET = %d, want 200", code)
	}

	const admin = "admin@userspage.test"
	a, err := db.User.Create().SetEmail(admin).SetName(admin).SetPreferredUsername(admin).SetRole(user.RoleAdmin).Save(context.Background())
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("user_id", a.ID)
	if err := NewUsersHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil))).List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	var resp struct {
		Users []userItem `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var emails []string
	for _, u := range resp.Users {
		emails = append(emails, u.Email)
	}
	slices.Sort(emails)
	if want := []string{admin, person}; !slices.Equal(emails, want) {
		t.Errorf("GET /api/v1/users = %v, want %v — a service reached the Users page", emails, want)
	}
}

// Item 5: an app principal's event names the service AND the issuer that
// vouched for it, so one ServiceAccount name from two clusters is two
// identities. Negative: a person's event carries no actorIssuer at all.
func TestAuditLog_NamesTheServiceAndItsIssuer(t *testing.T) {
	db := testutil.NewTestDB(t)
	clusterA, signA := newServiceIssuer(t)
	clusterB, signB := newServiceIssuer(t)
	kc, signKC := newServiceIssuer(t)
	people := auth.ProviderSpec{IssuerURL: kc, Audiences: []string{"armada-orbital"}, UsernameClaim: "email", DefaultRole: "readonly"}
	viaClient := auth.ProviderSpec{
		IssuerURL: kc, ClientID: "aep-fleet-commander", Audiences: []string{"armada-orbital"},
		SelfClientID: "armada-orbital", UsernameClaim: "email", DelegatedRole: "readonly",
	}
	s := newServiceStack(t, db, clusterEntry(clusterA), clusterEntry(clusterB), firmwareClientSpec(kc), people, viaClient)

	const direct, delegated = "operator@issuer.test", "via-aep@issuer.test"
	for name, token := range map[string]string{
		"ServiceAccount, cluster A": signA(serviceAccountToken(clusterA, firmwareSA)),
		"ServiceAccount, cluster B": signB(serviceAccountToken(clusterB, firmwareSA)),
		"Keycloak client":           signKC(clientCredentialsToken(kc, "armada-firmware-svc")),
		"person":                    signKC(serviceTokenClaims(kc, map[string]any{"sub": "p1", "email": direct})),
		"person via client": signKC(serviceTokenClaims(kc, map[string]any{
			"sub": "p2", "email": delegated, "azp": "aep-fleet-commander", "client_id": "aep-fleet-commander",
		})),
	} {
		if code := s.do(t, http.MethodPost, "/api/v1/backup", token, `{}`); code != http.StatusForbidden {
			t.Fatalf("%s: POST /api/v1/backup = %d, want 403 so a refusal is recorded", name, code)
		}
	}
	const intruder = "system:serviceaccount:other:intruder"
	if code := s.do(t, http.MethodGet, "/api/v1/divergences", signA(serviceAccountToken(clusterA, intruder)), ""); code != http.StatusUnauthorized {
		t.Fatalf("unlisted ServiceAccount: GET = %d, want 401", code)
	}

	h := NewEventHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-log?limit=500", nil)
	rec := httptest.NewRecorder()
	if err := h.List(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audit-log = %d: %s", rec.Code, rec.Body.String())
	}
	var resp auditLogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var raw struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}

	type key struct{ operation, actor string }
	issuers := map[key][]string{}
	for _, ev := range resp.Events {
		for _, op := range ev.Operations {
			k := key{op, ev.Actor}
			issuers[k] = append(issuers[k], ev.ActorIssuer)
		}
	}
	check := func(k key, want ...string) {
		t.Helper()
		got := slices.Sorted(slices.Values(issuers[k]))
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s by %s: actorIssuer %q, want %q; events seen: %v", k.operation, k.actor, got, want, slices.Collect(maps.Keys(issuers)))
		}
	}
	check(key{"authorizationDenied", auth.AppPrincipalPrefix + firmwareSA}, clusterA, clusterB)
	check(key{"authorizationDenied", auth.AppPrincipalPrefix + "armada-firmware-svc"}, kc)
	check(key{"bearerAuthFailed", auth.AppPrincipalPrefix + intruder}, clusterA)
	check(key{"authorizationDenied", direct}, "")
	check(key{"authorizationDenied", delegated}, "")

	for _, ev := range raw.Events {
		if actor := ev["actor"]; actor == direct || actor == delegated {
			if v, present := ev["actorIssuer"]; present {
				t.Errorf("%s: event carries actorIssuer %v — only a service is marked with its issuer", actor, v)
			}
		}
	}
}
