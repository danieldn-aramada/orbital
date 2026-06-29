//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

func newDeviceOIDCHandler(t *testing.T, p *oidcProvider) *handler.OIDC {
	t.Helper()
	t.Chdir("../..") // template paths are relative to the repo root
	h, err := handler.NewOIDC(
		context.Background(),
		testDB,
		oidcSessionKeys,
		p.Server.URL,
		"test-client-id",
		"test-client-secret",
		p.Server.URL+"/callback",
		"",
		nil,  // logger
		nil,  // adminEmails
		true, // deviceCodeEnabled
	)
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	return h
}

// ── DeviceCodeStart ────────────────────────────────────────────────────────────

func TestDeviceCodeStart_RendersUserCode(t *testing.T) {
	p := newOIDCProvider(t)
	h := newDeviceOIDCHandler(t, p)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/auth/device", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.DeviceCodeStart(c); err != nil {
		t.Fatalf("DeviceCodeStart: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()

	// Page-specific data plumbed through deviceCodePageData.
	if !strings.Contains(body, "ABCD-1234") {
		t.Errorf("expected user_code ABCD-1234 in response body, got: %s", body)
	}
	if !strings.Contains(body, "test-device-code") {
		t.Errorf("expected device_code test-device-code in response body")
	}

	// Proves the page actually renders as a full HTML document (not just the
	// head fragment that would result from calling Execute() on the wrong
	// root template — a real foot-gun that broke once already).
	if !strings.HasPrefix(strings.TrimSpace(body), "<!DOCTYPE html>") {
		t.Errorf("expected full HTML document starting with <!DOCTYPE html>; got first 80 chars: %q", body[:min(80, len(body))])
	}

	// Proves `{{template "head.gohtml" .}}` resolved and the shared head
	// content was inlined — pick a marker that ONLY appears in head.gohtml.
	if !strings.Contains(body, "htmx-2.0.2.min.js") {
		t.Error("expected head.gohtml content to be included (looked for htmx script tag); the {{template \"head.gohtml\" .}} directive did not resolve")
	}

	// Proves cache-busting works on this page — orbital.js and main.css must
	// carry the ?v= query string so a redeploy bumps the URL. Without this,
	// browsers serve stale JS forever (the bug that lost us hours on AKS).
	// Note: shared.js is imported as a module by orbital.js, not loaded directly.
	if !strings.Contains(body, "orbital.js?v=") {
		t.Error("expected orbital.js to have ?v= cache-bust query string")
	}
	if !strings.Contains(body, "main.css?v=") {
		t.Error("expected main.css to have ?v= cache-bust query string")
	}
}

// ── DeviceCodePoll — non-success states ───────────────────────────────────────

func TestDeviceCodePoll_Pending(t *testing.T) {
	p := newOIDCProvider(t)
	p.DeviceTokenError = "authorization_pending"
	h := newDeviceOIDCHandler(t, p)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/device/poll", strings.NewReader(`{"device_code":"test-device-code"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.DeviceCodePoll(c); err != nil {
		t.Fatalf("DeviceCodePoll: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "pending" {
		t.Errorf("expected status=pending, got %q", resp["status"])
	}
}

func TestDeviceCodePoll_Expired(t *testing.T) {
	p := newOIDCProvider(t)
	p.DeviceTokenError = "expired_token"
	h := newDeviceOIDCHandler(t, p)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/device/poll", strings.NewReader(`{"device_code":"test-device-code"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.DeviceCodePoll(c); err != nil {
		t.Fatalf("DeviceCodePoll: %v", err)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "expired" {
		t.Errorf("expected status=expired, got %q", resp["status"])
	}
}

func TestDeviceCodePoll_MissingDeviceCode_ReturnsExpired(t *testing.T) {
	p := newOIDCProvider(t)
	h := newDeviceOIDCHandler(t, p)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/device/poll", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.DeviceCodePoll(c); err != nil {
		t.Fatalf("DeviceCodePoll: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "expired" {
		t.Errorf("expected status=expired, got %q", resp["status"])
	}
}

// ── DeviceCodePoll — success paths ───────────────────────────────────────────

func TestDeviceCodePoll_Complete_NewUser(t *testing.T) {
	const testEmail = "device-code-new@example.com"
	ctx := context.Background()

	p := newOIDCProvider(t)
	p.TokenClaims["email"] = testEmail
	p.TokenClaims["preferred_username"] = testEmail
	p.TokenClaims["name"] = "Device Code User"
	h := newDeviceOIDCHandler(t, p)

	t.Cleanup(func() {
		testDB.User.Delete().Where(user.Email(testEmail)).ExecX(ctx)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/device/poll", strings.NewReader(`{"device_code":"test-device-code"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.DeviceCodePoll(c); err != nil {
		t.Fatalf("DeviceCodePoll: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "complete" {
		t.Errorf("expected status=complete, got %q", resp["status"])
	}

	// User should be provisioned in DB.
	u, err := testDB.User.Query().Where(user.Email(testEmail)).Only(ctx)
	if err != nil {
		t.Fatalf("user not found after device code provisioning: %v", err)
	}
	if u.Name != "Device Code User" {
		t.Errorf("user name: got %q, want %q", u.Name, "Device Code User")
	}
	if !u.Verified {
		t.Error("provisioned device code user should be verified=true")
	}

	// Session cookie should be set.
	var sessionFound bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "orbital_session" {
			sessionFound = true
		}
	}
	if !sessionFound {
		t.Error("expected orbital_session cookie after successful device code poll")
	}
}

func TestDeviceCodePoll_Complete_ExistingUser(t *testing.T) {
	const testEmail = "device-code-existing@example.com"
	ctx := context.Background()

	existing := testDB.User.Create().
		SetEmail(testEmail).
		SetName("Existing Device User").
		SetPreferredUsername(testEmail).
		SetVerified(true).
		SaveX(ctx)
	t.Cleanup(func() { testDB.User.DeleteOne(existing).ExecX(ctx) })

	p := newOIDCProvider(t)
	p.TokenClaims["email"] = testEmail
	p.TokenClaims["preferred_username"] = testEmail
	h := newDeviceOIDCHandler(t, p)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/device/poll", strings.NewReader(`{"device_code":"test-device-code"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.DeviceCodePoll(c); err != nil {
		t.Fatalf("DeviceCodePoll: %v", err)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "complete" {
		t.Errorf("expected status=complete, got %q", resp["status"])
	}

	// No duplicate should be created.
	count := testDB.User.Query().Where(user.Email(testEmail)).CountX(ctx)
	if count != 1 {
		t.Errorf("expected exactly 1 user with email %q, got %d", testEmail, count)
	}
}
