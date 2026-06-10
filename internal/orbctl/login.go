package orbctl

import (
	"fmt"

	"github.com/armada/orbital/internal/cli/out"
	"github.com/armada/orbital/internal/orbauth"
	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Orbital using your Microsoft account",
	RunE:  runLogin,
}

func runLogin(cmd *cobra.Command, args []string) error {
	store, err := orbauth.OrbitalFileStore()
	if err != nil {
		return err
	}

	// Already valid access token on disk — nothing to do.
	if creds, _ := orbauth.LoadValid(store); creds != nil {
		out.Success(fmt.Sprintf("Already signed in as %s (%s)", creds.Name, creds.Email))
		if verbose {
			printToken(creds.AccessToken)
		}
		return nil
	}

	// Access token expired/missing — try silent refresh if we have a refresh token.
	if saved, err := store.Load(); err == nil && saved.RefreshToken != "" {
		sp := out.Spinner(fmt.Sprintf("Signing in as %s", saved.Email))
		newCreds, err := orbauth.RefreshToken(saved.RefreshToken, saved.Name, saved.Email)
		if err == nil {
			if err := store.Save(newCreds); err != nil {
				out.Warning("Could not save refreshed credentials: " + err.Error())
			}
			sp.Stop(fmt.Sprintf("Signed in as %s (%s)", newCreds.Name, newCreds.Email))
			if verbose {
				printToken(newCreds.AccessToken)
			}
			return nil
		}
		sp.Fail("Token refresh failed — re-authenticating")
	}

	// Full browser PKCE flow. BrowserLogin saves the full credentials to store.
	out.Step("🌐", "Opening browser for authentication...")
	sp := out.Spinner("Waiting for authentication")
	creds, err := orbauth.BrowserLogin(cmd.Context(), out.Writer, store)
	if err != nil {
		sp.Fail("Authentication failed")
		return err
	}
	sp.Stop(fmt.Sprintf("Signed in as %s (%s)", creds.Name, creds.Email))
	if verbose {
		printToken(creds.AccessToken)
	}
	return nil
}

func printToken(token string) {
	fmt.Fprintln(out.Writer)
	fmt.Fprintf(out.Writer, "  export ORBITAL_TOKEN=%s\n", token)
	fmt.Fprintln(out.Writer)
}
