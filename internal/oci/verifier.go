package oci

import (
	"context"
	"crypto"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/go-containerregistry/pkg/name"
	cremote "github.com/google/go-containerregistry/pkg/v1/remote"
	cosignpkg "github.com/sigstore/cosign/v2/pkg/cosign"
	ociremote "github.com/sigstore/cosign/v2/pkg/oci/remote"
	"github.com/sigstore/sigstore/pkg/signature"
)

// VerifyConfig holds parameters for cosign signature verification.
type VerifyConfig struct {
	// PublicKeyPath is the path to cosign.pub (PEM-encoded ECDSA public key). Required.
	PublicKeyPath string
	AllowHTTP     bool
}

// VerifyResult summarises the outcome of a signature verification attempt.
type VerifyResult struct {
	Verified    bool
	Fingerprint string // key fingerprint, if verified
	Message     string // human-readable summary for UI display
}

// Verify checks the cosign signature on the OCI artifact at repoRef@digest.
// repoRef is "<registry>/<repo>/<dc-slug>"; digest is the manifest digest string.
func Verify(ctx context.Context, cfg VerifyConfig, repoRef, digest string, logger *slog.Logger) (*VerifyResult, error) {
	if cfg.PublicKeyPath == "" {
		return nil, fmt.Errorf("ORB_OCI_PUBLIC_KEY_PATH is not configured — signature verification is required")
	}

	// Load the public key verifier directly from the PEM file.
	verifier, err := signature.LoadVerifierFromPEMFile(cfg.PublicKeyPath, crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("load public key from %s: %w", cfg.PublicKeyPath, err)
	}

	checkOpts := &cosignpkg.CheckOpts{
		SigVerifier: verifier,
		IgnoreTlog:  true,
		IgnoreSCT:   true,
	}

	// For local plain-HTTP registries (Zot in dev), allow insecure connections.
	if cfg.AllowHTTP {
		checkOpts.RegistryClientOpts = []ociremote.Option{
			ociremote.WithRemoteOptions(cremote.WithTransport(http.DefaultTransport)),
		}
	}

	// Build digest reference: "<registry>/<repo>/<dc-slug>@<digest>"
	fullRef := repoRef + "@" + digest
	var nameOpts []name.Option
	if cfg.AllowHTTP {
		nameOpts = append(nameOpts, name.Insecure)
	}
	ref, err := name.NewDigest(fullRef, nameOpts...)
	if err != nil {
		return nil, fmt.Errorf("parse digest ref %q: %w", fullRef, err)
	}

	sigs, _, err := cosignpkg.VerifyImageSignatures(ctx, ref, checkOpts)
	if err != nil {
		return nil, fmt.Errorf("verify signatures: %w", err)
	}
	if len(sigs) == 0 {
		return nil, fmt.Errorf("no valid signatures found for %s", fullRef)
	}

	// Compute fingerprint from the public key for display.
	var fingerprint string
	if pub, pkErr := verifier.PublicKey(); pkErr == nil {
		if fp, fpErr := PublicKeyFingerprint(pub); fpErr == nil {
			fingerprint = fp
		}
	}
	if fingerprint == "" {
		logger.Warn("could not compute key fingerprint")
	}

	short := fingerprint
	if len(fingerprint) > 12 {
		short = fingerprint[:12]
	}
	return &VerifyResult{
		Verified:    true,
		Fingerprint: fingerprint,
		Message:     fmt.Sprintf("Verified — key fingerprint %s", short),
	}, nil
}
