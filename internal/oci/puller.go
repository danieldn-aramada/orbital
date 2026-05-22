package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// PullConfig holds configuration for pulling artifacts from an OCI registry.
// Repo is the full repository path, e.g. "orbital/colo-galleon". DC identity
// is encoded in Repo by the operator — not as a separate field.
type PullConfig struct {
	Registry  string
	Repo      string
	Username  string
	Password  string
	AllowHTTP bool
}

// PulledArtifact contains the data extracted from a pulled OCI artifact.
type PulledArtifact struct {
	DataGZ      []byte
	SchemaGZ    []byte
	Annotations map[string]string
	Digest      string // manifest digest, used for cosign verification
	Tag         string
}

// ListTags returns all tags available for this DC's repo in the registry, newest first.
func ListTags(ctx context.Context, cfg PullConfig) ([]string, error) {
	repo, err := newPullRepo(repoRef(cfg))
	if err != nil {
		return nil, fmt.Errorf("new repo: %w", err)
	}
	repo.PlainHTTP = cfg.AllowHTTP
	setCredentials(repo, cfg.Registry, cfg.Username, cfg.Password)

	var tags []string
	if err := repo.Tags(ctx, "", func(t []string) error {
		tags = append(tags, t...)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return tags, nil
}

// Pull downloads a specific tag's artifact from the registry and returns its contents.
func Pull(ctx context.Context, cfg PullConfig, tag string) (*PulledArtifact, error) {
	ref := repoRef(cfg)
	repo, err := newPullRepo(ref)
	if err != nil {
		return nil, fmt.Errorf("new repo: %w", err)
	}
	repo.PlainHTTP = cfg.AllowHTTP
	setCredentials(repo, cfg.Registry, cfg.Username, cfg.Password)

	store := memory.New()
	manifestDesc, err := oras.Copy(ctx, repo, tag, store, tag, oras.DefaultCopyOptions)
	if err != nil {
		return nil, fmt.Errorf("oras copy: %w", err)
	}

	// Fetch and parse the manifest to extract layers and annotations.
	rc, err := store.Fetch(ctx, manifestDesc)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	manifestBytes, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	artifact := &PulledArtifact{
		Annotations: manifest.Annotations,
		Digest:      manifestDesc.Digest.String(),
		Tag:         tag,
	}

	// Extract data.json.gz and schema.gz by mediaType.
	for _, layer := range manifest.Layers {
		rc, err := store.Fetch(ctx, layer)
		if err != nil {
			return nil, fmt.Errorf("fetch layer %s: %w", layer.MediaType, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read layer %s: %w", layer.MediaType, err)
		}
		switch layer.MediaType {
		case mediaTypeDataGZ:
			artifact.DataGZ = data
		case mediaTypeSchemaGZ:
			artifact.SchemaGZ = data
		}
	}

	if artifact.DataGZ == nil {
		return nil, fmt.Errorf("artifact missing data layer (mediaType %s)", mediaTypeDataGZ)
	}
	if artifact.SchemaGZ == nil {
		return nil, fmt.Errorf("artifact missing schema layer (mediaType %s)", mediaTypeSchemaGZ)
	}

	return artifact, nil
}

// repoRef builds the full repository reference: "<registry>/<repo>"
func repoRef(cfg PullConfig) string {
	return cfg.Registry + "/" + cfg.Repo
}

func newPullRepo(ref string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, err
	}
	return repo, nil
}

func setCredentials(repo *remote.Repository, host, username, password string) {
	if username == "" && password == "" {
		return
	}
	repo.Client = &orasauth.Client{
		Client:     retry.DefaultClient,
		Cache:      orasauth.NewCache(),
		Credential: orasauth.StaticCredential(host, orasauth.Credential{Username: username, Password: password}),
	}
}
