package orbauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	TenantID = "8f231c2a-9551-4b40-be17-5b24afe5e890"
	ClientID = "5fc832f6-843e-4207-93dd-b3c3a77c06f2"
	Scope    = "api://5fc832f6-843e-4207-93dd-b3c3a77c06f2/user_impersonation offline_access"
)

// Credentials holds the tokens and identity information obtained after login.
type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
}

// Valid reports whether the access token has more than 60 seconds remaining.
func (c *Credentials) Valid() bool {
	return c.ExpiresAt.After(time.Now().Add(60 * time.Second))
}

// LoadValid returns stored credentials if they are present and not expired.
// Returns nil, nil when credentials are missing or expired.
func LoadValid(store Store) (*Credentials, error) {
	creds, err := store.Load()
	if err != nil || !creds.Valid() {
		return nil, nil
	}
	return creds, nil
}

// BrowserLogin runs the Authorization Code + PKCE flow, saves credentials to
// store, and returns them. w receives the fallback URL line so the caller can
// display it; pass io.Discard to suppress it.
func BrowserLogin(ctx context.Context, w io.Writer, store Store) (*Credentials, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start local server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d", port)

	codeVerifier, err := RandomString(64)
	if err != nil {
		return nil, fmt.Errorf("generate code verifier: %w", err)
	}
	h := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

	state, err := RandomString(16)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	authURL := "https://login.microsoftonline.com/" + TenantID + "/oauth2/v2.0/authorize?" +
		url.Values{
			"client_id":             {ClientID},
			"response_type":         {"code"},
			"redirect_uri":          {redirectURI},
			"scope":                 {Scope},
			"state":                 {state},
			"code_challenge":        {codeChallenge},
			"code_challenge_method": {"S256"},
		}.Encode()

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("state mismatch — possible CSRF")}
			return
		}
		if e := q.Get("error"); e != "" {
			fmt.Fprintf(w, "<html><body><p>Authentication failed: %s. You can close this tab.</p></body></html>", q.Get("error_description"))
			ch <- result{err: fmt.Errorf("%s: %s", e, q.Get("error_description"))}
			return
		}
		fmt.Fprint(w, "<html><body><p>Authentication successful. You can close this tab.</p></body></html>")
		ch <- result{code: q.Get("code")}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener) //nolint:errcheck
	defer srv.Shutdown(context.Background()) //nolint:errcheck

	fmt.Fprintf(w, "\n    If your browser doesn't open, visit:\n    %s\n\n", authURL)
	OpenBrowser(authURL)

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		creds, err := ExchangeAuthCode(r.code, codeVerifier, redirectURI)
		if err != nil {
			return nil, fmt.Errorf("token exchange: %w", err)
		}
		if store != nil {
			_ = store.Save(creds)
		}
		return creds, nil
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("authentication timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ExchangeAuthCode exchanges an authorization code for credentials.
func ExchangeAuthCode(code, codeVerifier, redirectURI string) (*Credentials, error) {
	return tokenRequest(url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {codeVerifier},
	})
}

// RefreshToken exchanges a refresh token for new credentials, carrying over
// name and email from the previous credentials if not returned by the server.
func RefreshToken(refreshToken, name, email string) (*Credentials, error) {
	creds, err := tokenRequest(url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {refreshToken},
		"scope":         {Scope},
	})
	if err != nil {
		return nil, err
	}
	if creds.Name == "" {
		creds.Name = name
	}
	if creds.Email == "" {
		creds.Email = email
	}
	return creds, nil
}

func tokenRequest(values url.Values) (*Credentials, error) {
	resp, err := http.PostForm(
		"https://login.microsoftonline.com/"+TenantID+"/oauth2/v2.0/token",
		values,
	)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("%s: %s", result.Error, result.ErrorDesc)
	}

	name, email := JWTDisplayClaims(result.AccessToken)
	return &Credentials{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(result.ExpiresIn) * time.Second),
		Name:         name,
		Email:        email,
	}, nil
}

// JWTDisplayClaims extracts name and email from a JWT payload without
// validating the signature. Used only for display after a successful login.
func JWTDisplayClaims(token string) (name, email string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "unknown", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "unknown", ""
	}
	var claims struct {
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		UPN               string `json:"upn"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "unknown", ""
	}
	email = claims.PreferredUsername
	if email == "" {
		email = claims.UPN
	}
	return claims.Name, email
}

// OpenBrowser opens u in the default browser for the current OS.
func OpenBrowser(u string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{u}
	case "windows":
		cmd, args = "cmd", []string{"/c", "start", u}
	default:
		cmd, args = "xdg-open", []string{u}
	}
	exec.Command(cmd, args...).Start() //nolint:errcheck
}

// RandomString returns a URL-safe random string of exactly n characters.
func RandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:n], nil
}
