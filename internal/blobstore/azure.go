package blobstore

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
)

type azureStore struct {
	client      *azblob.Client
	svcClient   *service.Client
	container   string
	accountName string
	accountKey  string
}

func newAzureStore(endpoint, accountName, accountKey, container string) (*azureStore, error) {
	cred, err := azblob.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("blobstore azure: shared key credential: %w", err)
	}
	client, err := azblob.NewClientWithSharedKeyCredential(endpoint, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("blobstore azure: blob client: %w", err)
	}
	svcCred, err := service.NewClientWithSharedKeyCredential(endpoint, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("blobstore azure: service client: %w", err)
	}
	return &azureStore{
		client:      client,
		svcClient:   svcCred,
		container:   container,
		accountName: accountName,
		accountKey:  accountKey,
	}, nil
}

func (a *azureStore) Put(ctx context.Context, key string, body io.Reader, contentType string) error {
	// UploadStream takes io.Reader; content-type isn't directly supported here
	// (azblob applies it via HTTPHeaders in opts), but callers in this repo
	// either send JSON via Publisher (no content-type required by Azure
	// defaults) or zip files via Backup (Azure infers application/zip). If
	// content-type ever becomes load-bearing, switch to UploadBuffer with
	// explicit HTTPHeaders.
	_, err := a.client.UploadStream(ctx, a.container, key, body, nil)
	return err
}

func (a *azureStore) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := a.client.DownloadStream(ctx, a.container, key, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (a *azureStore) List(ctx context.Context, prefix string) ([]string, error) {
	p := prefix
	opts := &container.ListBlobsFlatOptions{Prefix: &p}
	pager := a.client.NewListBlobsFlatPager(a.container, opts)
	var keys []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, blob := range page.Segment.BlobItems {
			if blob.Name != nil {
				keys = append(keys, *blob.Name)
			}
		}
	}
	return keys, nil
}

func (a *azureStore) Delete(ctx context.Context, key string) error {
	_, err := a.client.DeleteBlob(ctx, a.container, key, nil)
	return err
}

func (a *azureStore) PresignURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	cred, err := azblob.NewSharedKeyCredential(a.accountName, a.accountKey)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	sasQueryParams, err := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPS,
		StartTime:     now,
		ExpiryTime:    now.Add(ttl),
		Permissions:   blobReadPermissions(),
		ContainerName: a.container,
		BlobName:      key,
	}.SignWithSharedKey(cred)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%s?%s", a.svcClient.URL(), a.container, key, sasQueryParams.Encode()), nil
}

func (a *azureStore) Ping(ctx context.Context) error {
	pager := a.client.NewListBlobsFlatPager(a.container, nil)
	_, err := pager.NextPage(ctx)
	return err
}

func blobReadPermissions() string {
	return (&sas.BlobPermissions{Read: true}).String()
}
