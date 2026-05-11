package orbitalcli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/armada/orbital/internal/cli/out"
	"github.com/armada/orbital/internal/orbauth"
	"github.com/spf13/cobra"
)

var verbose bool

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Orbital using your Microsoft account",
	RunE:  runLogin,
}

func init() {
	loginCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Print the access token after login")
}

func runLogin(cmd *cobra.Command, args []string) error {
	fileStore, err := orbauth.OrbitalFileStore()
	if err != nil {
		return err
	}

	// Step 1: valid access token already on disk — nothing to do.
	if creds, _ := orbauth.LoadValid(fileStore); creds != nil {
		out.Success(fmt.Sprintf("Already signed in as %s (%s)", creds.Name, creds.Email))
		if verbose {
			printToken(creds.AccessToken)
		}
		return nil
	}

	// Step 2: access token expired/missing — use refresh token from keychain.
	kcStore := keychainStore()
	if saved, err := kcStore.Load(); err == nil && saved.RefreshToken != "" {
		sp := out.Spinner(fmt.Sprintf("Signing in as %s", saved.Email))
		newCreds, err := orbauth.RefreshToken(saved.RefreshToken, saved.Name, saved.Email)
		if err == nil {
			saveSession(fileStore, kcStore, newCreds)
			sp.Stop(fmt.Sprintf("Signed in as %s (%s)", newCreds.Name, newCreds.Email))
			if verbose {
				printToken(newCreds.AccessToken)
			}
			return nil
		}
		sp.Fail("Token refresh failed — re-authenticating")
	}

	// Step 3: no refresh token — full browser PKCE flow.
	out.Step("🌐", "Opening browser for authentication...")
	sp := out.Spinner("Waiting for authentication")
	// BrowserLogin saves the refresh token to kcStore internally.
	creds, err := orbauth.BrowserLogin(cmd.Context(), out.Writer, kcStore)
	if err != nil {
		sp.Fail("Authentication failed")
		return err
	}
	// Save access token separately to the file store (refresh token already in keychain).
	if err := fileStore.Save(sessionCreds(creds)); err != nil {
		out.Warning("Could not save session: " + err.Error())
	}
	sp.Stop(fmt.Sprintf("Signed in as %s (%s)", creds.Name, creds.Email))
	if verbose {
		printToken(creds.AccessToken)
	}
	return nil
}

// saveSession writes the access token to the file store and updates the
// refresh token in the keychain (Azure AD may rotate it on each refresh).
func saveSession(fileStore *orbauth.FileStore, kcStore orbauth.Store, creds *orbauth.Credentials) {
	if err := fileStore.Save(sessionCreds(creds)); err != nil {
		out.Warning("Could not save session: " + err.Error())
	}
	if err := kcStore.Save(creds); err != nil {
		out.Warning("Could not update keychain: " + err.Error())
	}
}

// sessionCreds returns a Credentials with only the access token and identity
// fields — no refresh token — suitable for writing to the file store.
func sessionCreds(creds *orbauth.Credentials) *orbauth.Credentials {
	return &orbauth.Credentials{
		AccessToken: creds.AccessToken,
		ExpiresAt:   creds.ExpiresAt,
		Name:        creds.Name,
		Email:       creds.Email,
	}
}

// keychainStore returns a Store backed by the OS keychain with a file fallback
// at ~/.orbital/.keychain-fallback.json for headless / CI environments.
// On macOS this is a Touch ID / device passcode protected keychain entry;
// on other platforms it uses the system keyring.
func keychainStore() orbauth.Store {
	home, _ := os.UserHomeDir()
	fallbackPath := filepath.Join(home, ".orbital", ".keychain-fallback.json")
	return orbauth.NewKeychainStore(&orbauth.FileStore{Path: fallbackPath})
}

func printToken(token string) {
	fmt.Fprintln(out.Writer)
	fmt.Fprintf(out.Writer, "  export ORBITAL_TOKEN=%s\n", token)
	fmt.Fprintln(out.Writer)
}
