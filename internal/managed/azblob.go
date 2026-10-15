// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

// AzureBlob reads state from Azure Blob Storage, where Terraform's azurerm
// backend keeps it: containers are buckets.
type AzureBlob struct {
	client *azblob.Client
}

// NewAzureBlob opens the Blob Storage account at serviceURL
// (https://<account>.blob.core.windows.net/) with the credentials the
// azurerm backend takes from the environment: the account's access key
// (ARM_ACCESS_KEY), a SAS token (ARM_SAS_TOKEN), or a service principal
// (ARM_CLIENT_ID, ARM_CLIENT_SECRET and ARM_TENANT_ID), and otherwise
// Azure's default credential chain, such as the Azure CLI's login. All but
// the first two need a data role on the account, such as Storage Blob Data
// Reader.
func NewAzureBlob(_ context.Context, serviceURL string) (ObjectStore, error) {
	client, err := azureBlobClient(serviceURL, os.Getenv)
	if err != nil {
		return nil, err
	}
	return &AzureBlob{client: client}, nil
}

func azureBlobClient(serviceURL string, getenv func(string) string) (*azblob.Client, error) {
	if key := getenv("ARM_ACCESS_KEY"); key != "" {
		u, err := url.Parse(serviceURL)
		if err != nil {
			return nil, err
		}
		account, _, _ := strings.Cut(u.Host, ".")
		credential, err := azblob.NewSharedKeyCredential(account, key)
		if err != nil {
			return nil, err
		}
		return azblob.NewClientWithSharedKeyCredential(serviceURL, credential, nil)
	}
	if sas := strings.TrimPrefix(getenv("ARM_SAS_TOKEN"), "?"); sas != "" {
		return azblob.NewClientWithNoCredential(serviceURL+"?"+sas, nil)
	}
	var credential azcore.TokenCredential
	var err error
	clientID, secret, tenant := getenv("ARM_CLIENT_ID"), getenv("ARM_CLIENT_SECRET"), getenv("ARM_TENANT_ID")
	if clientID != "" && secret != "" && tenant != "" {
		credential, err = azidentity.NewClientSecretCredential(tenant, clientID, secret, nil)
	} else {
		credential, err = azidentity.NewDefaultAzureCredential(nil)
	}
	if err != nil {
		return nil, err
	}
	return azblob.NewClient(serviceURL, credential, nil)
}

// List returns the names of the blobs under prefix in container.
func (a *AzureBlob) List(ctx context.Context, container, prefix string) ([]string, error) {
	var names []string
	pages := a.client.NewListBlobsFlatPager(container, &azblob.ListBlobsFlatOptions{Prefix: &prefix})
	for pages.More() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range page.Segment.BlobItems {
			if b != nil && b.Name != nil {
				names = append(names, *b.Name)
			}
		}
	}
	return names, nil
}

// Get returns a blob's content.
func (a *AzureBlob) Get(ctx context.Context, container, name string) ([]byte, error) {
	resp, err := a.client.DownloadStream(ctx, container, name, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
