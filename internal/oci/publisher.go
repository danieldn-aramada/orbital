package oci

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/google/go-containerregistry/pkg/authn"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	cosigncli "github.com/sigstore/cosign/v2/cmd/cosign/cli/sign"
	cosignopt "github.com/sigstore/cosign/v2/cmd/cosign/cli/options"
	cosign "github.com/sigstore/cosign/v2/pkg/cosign"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const (
	artifactType      = "application/vnd.orbital.subgraph.v1"
	mediaTypeDataGZ   = "application/vnd.orbital.subgraph.data.v1+gzip"
	mediaTypeSchemaGZ = "application/vnd.orbital.subgraph.schema.v1+gzip"

	annotationCreated     = "org.opencontainers.image.created"
	annotationVersion     = "org.opencontainers.image.version"
	annotationDCID        = "com.armada.orbital.datacenter-id"
	annotationExportJobID = "com.armada.orbital.export-job-id"
	annotationPubKeyURL   = "com.armada.orbital.cosign-public-key-url"
)

// Config holds OCI publishing configuration derived from env vars.
type Config struct {
	Registry      string
	Repo          string
	Username      string
	Password      string
	SigningKeyPath string
	// Host is the orbital server's external hostname for the public key hint annotation (optional).
	Host string
	// AllowHTTP enables plain-HTTP (non-TLS) registry connections. For local testing only.
	AllowHTTP bool
}

// Publisher pushes subgraph exports as signed OCI artifacts.
type Publisher struct {
	db     *ent.Client
	cfg    Config
	logger *slog.Logger
}

// New creates a Publisher.
func New(db *ent.Client, cfg Config, logger *slog.Logger) *Publisher {
	return &Publisher{db: db, cfg: cfg, logger: logger}
}

// Publish executes an async publish for the given registry_artifact row.
// Intended to be called as a goroutine; updates the row in PostgreSQL as it progresses.
func (p *Publisher) Publish(artifactID int, job *ent.ExportJob, tag string) {
	ctx := context.Background()
	log := p.logger.With("artifactId", artifactID, "jobId", job.ID, "tag", tag)

	if _, err := p.db.RegistryArtifact.UpdateOneID(artifactID).
		SetStatus(registryartifact.StatusPushing).
		Save(ctx); err != nil {
		log.Error("failed to mark artifact pushing", "err", err)
		return
	}

	digest, sizeBytes, fingerprint, err := p.doPush(ctx, job, tag, log)
	if err != nil {
		log.Error("publish failed", "err", err)
		errStr := err.Error()
		p.db.RegistryArtifact.UpdateOneID(artifactID). //nolint:errcheck
			SetStatus(registryartifact.StatusFailed).
			SetError(errStr).
			SetCompletedAt(time.Now()).
			Save(ctx)
		return
	}

	update := p.db.RegistryArtifact.UpdateOneID(artifactID).
		SetStatus(registryartifact.StatusCompleted).
		SetDigest(digest).
		SetSizeBytes(sizeBytes).
		SetSigned(true).
		SetCompletedAt(time.Now())
	if fingerprint != "" {
		update = update.SetSigningKeyFingerprint(fingerprint)
	}
	if _, err := update.Save(ctx); err != nil {
		log.Error("failed to mark artifact completed", "err", err)
	}
}

func (p *Publisher) doPush(ctx context.Context, job *ent.ExportJob, tag string, log *slog.Logger) (digest string, sizeBytes int64, fingerprint string, err error) {
	if job.ArtifactPath == nil {
		return "", 0, "", fmt.Errorf("export job has no artifact path")
	}

	dataGZ, schemaGZ, err := extractZip(*job.ArtifactPath)
	if err != nil {
		return "", 0, "", fmt.Errorf("extract zip: %w", err)
	}
	log.Info("extracted zip", "dataBytes", len(dataGZ), "schemaBytes", len(schemaGZ))

	repoName := RepoForDC(p.cfg.Registry, p.cfg.Repo, job.DatacenterName)
	log.Info("target repository", "repo", repoName)

	manifestDesc, err := p.pushArtifact(ctx, repoName, tag, dataGZ, schemaGZ, job, log)
	if err != nil {
		return "", 0, "", fmt.Errorf("push artifact: %w", err)
	}
	digestStr := manifestDesc.Digest.String()
	log.Info("artifact pushed", "digest", digestStr, "tag", tag)

	fingerprint, err = p.sign(ctx, repoName, digestStr, log)
	if err != nil {
		return "", 0, "", fmt.Errorf("sign: %w", err)
	}
	log.Info("artifact signed", "fingerprint", fingerprint)

	return digestStr, manifestDesc.Size, fingerprint, nil
}

func (p *Publisher) pushArtifact(ctx context.Context, repoName, tag string, dataGZ, schemaGZ []byte, job *ent.ExportJob, log *slog.Logger) (ocispec.Descriptor, error) {
	store := memory.New()

	dataDesc, err := pushBlob(ctx, store, mediaTypeDataGZ, dataGZ)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push data blob: %w", err)
	}
	schemaDesc, err := pushBlob(ctx, store, mediaTypeSchemaGZ, schemaGZ)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push schema blob: %w", err)
	}

	annotations := map[string]string{
		annotationCreated:     time.Now().UTC().Format(time.RFC3339),
		annotationVersion:     tag,
		annotationDCID:        job.DatacenterID,
		annotationExportJobID: job.ID.String(),
	}
	if p.cfg.Host != "" {
		annotations[annotationPubKeyURL] = "https://" + p.cfg.Host + "/api/v1/oci/public-key"
	}

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, artifactType, oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{dataDesc, schemaDesc},
		ManifestAnnotations: annotations,
	})
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("pack manifest: %w", err)
	}

	repo, err := p.newRepo(repoName)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("new repo: %w", err)
	}

	if err := store.Tag(ctx, manifestDesc, tag); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("tag manifest: %w", err)
	}
	if _, err := oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("oras copy: %w", err)
	}

	if err := repo.Tag(ctx, manifestDesc, "latest"); err != nil {
		log.Warn("failed to update :latest tag", "err", err)
	}

	return manifestDesc, nil
}

func (p *Publisher) sign(ctx context.Context, repoName, digestStr string, log *slog.Logger) (string, error) {
	ref := repoName + "@" + digestStr

	ko := cosignopt.KeyOpts{
		KeyRef:           p.cfg.SigningKeyPath,
		SkipConfirmation: true,
		PassFunc:         func(bool) ([]byte, error) { return []byte{}, nil },
	}
	signOpts := cosignopt.SignOptions{
		Upload:           true,
		TlogUpload:       false,
		SkipConfirmation: true,
		Registry: cosignopt.RegistryOptions{
			AllowInsecure:     p.cfg.AllowHTTP,
			AllowHTTPRegistry: p.cfg.AllowHTTP,
			AuthConfig: authn.AuthConfig{
				Username: p.cfg.Username,
				Password: p.cfg.Password,
			},
		},
	}
	ro := &cosignopt.RootOptions{Timeout: cosignopt.DefaultTimeout}

	if err := cosigncli.SignCmd(ro, ko, signOpts, []string{ref}); err != nil {
		return "", fmt.Errorf("cosign sign: %w", err)
	}

	fingerprint, err := p.keyFingerprint()
	if err != nil {
		log.Warn("could not compute key fingerprint", "err", err)
		return "", nil
	}
	return fingerprint, nil
}

func (p *Publisher) keyFingerprint() (string, error) {
	keyPEM, err := os.ReadFile(p.cfg.SigningKeyPath)
	if err != nil {
		return "", fmt.Errorf("read signing key: %w", err)
	}
	sv, err := cosign.LoadPrivateKey(keyPEM, []byte{}, nil)
	if err != nil {
		return "", fmt.Errorf("load private key: %w", err)
	}
	pub, err := sv.PublicKey()
	if err != nil {
		return "", fmt.Errorf("get public key: %w", err)
	}
	return PublicKeyFingerprint(pub)
}

func (p *Publisher) newRepo(repoName string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(repoName)
	if err != nil {
		return nil, err
	}
	repo.PlainHTTP = p.cfg.AllowHTTP
	cred := orasauth.Credential{
		Username: p.cfg.Username,
		Password: p.cfg.Password,
	}
	repo.Client = &orasauth.Client{
		Client:     retry.DefaultClient,
		Cache:      orasauth.NewCache(),
		Credential: orasauth.StaticCredential(repo.Reference.Host(), cred),
	}
	return repo, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func pushBlob(ctx context.Context, store *memory.Store, mediaType string, data []byte) (ocispec.Descriptor, error) {
	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    godigest.FromBytes(data),
		Size:      int64(len(data)),
	}
	return desc, store.Push(ctx, desc, bytes.NewReader(data))
}

func extractZip(zipPath string) (dataGZ, schemaGZ []byte, err error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("open %s in zip: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("read %s in zip: %w", f.Name, err)
		}
		switch f.Name {
		case "data.json.gz":
			dataGZ = data
		case "schema.gz":
			schemaGZ = data
		}
	}

	if dataGZ == nil {
		return nil, nil, fmt.Errorf("data.json.gz not found in zip")
	}
	if schemaGZ == nil {
		return nil, nil, fmt.Errorf("schema.gz not found in zip")
	}
	return dataGZ, schemaGZ, nil
}

var nonAlphanumHyphen = regexp.MustCompile(`[^a-z0-9-]`)

// RepoForDC builds the full repository reference for a data center.
// e.g. myregistry.azurecr.io/orbital/alaska-dot
func RepoForDC(registry, repoPrefix, dcName string) string {
	slug := strings.ToLower(dcName)
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = nonAlphanumHyphen.ReplaceAllString(slug, "")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "dc"
	}
	return registry + "/" + repoPrefix + "/" + slug
}

// PublicKeyFingerprint returns the hex-encoded SHA256 of the PKIX PEM-encoded public key.
func PublicKeyFingerprint(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	h := sha256.Sum256(pubPEM)
	return hex.EncodeToString(h[:]), nil
}

// PublicKeyPEM reads the signing key and returns the raw public key PEM.
// Used by the GET /api/v1/oci/public-key handler.
func PublicKeyPEM(signingKeyPath string) ([]byte, error) {
	keyPEM, err := os.ReadFile(signingKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	sv, err := cosign.LoadPrivateKey(keyPEM, []byte{}, nil)
	if err != nil {
		return nil, fmt.Errorf("load private key: %w", err)
	}
	pub, err := sv.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("get public key: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// NextTag returns the next version tag based on the count of existing artifacts for a DC.
func NextTag(count int) string {
	return fmt.Sprintf("v%d", count+1)
}
