// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package azblob reads Terraform state from Azure Blob Storage, where the
// azurerm backend keeps it. It links the Azure SDK, so only a build with
// Azure includes it.
package azblob

import (
	"context"
	"net/url"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	sdkazblob "github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/IgnatG/infraharvest/internal/managed"
)

// Store reads state from Azure Blob Storage: containers are buckets.
type Store struct {
	client *sdkazblob.Client
}

// New opens the Blob Storage account at serviceURL
// (https://<account>.blob.core.windows.net/) with the credentials the
// azurerm backend takes from the environment: the account's access key
// (ARM_ACCESS_KEY), a SAS token (ARM_SAS_TOKEN), or a service principal
// (ARM_CLIENT_ID, ARM_CLIENT_SECRET and ARM_TENANT_ID), and otherwise
// Azure's default credential chain, such as the Azure CLI's login. All but
// the first two need a data role on the account, such as Storage Blob Data
// Reader.
func New(_ context.Context, serviceURL string) (managed.ObjectStore, error) {
	client, err := newClient(serviceURL, os.Getenv)
	if err != nil {
		return nil, err
	}
	return &Store{client: client}, nil
}

func newClient(serviceURL string, getenv func(string) string) (*sdkazblob.Client, error) {
	if key := getenv("ARM_ACCESS_KEY"); key != "" {
		u, err := url.Parse(serviceURL)
		if err != nil {
			return nil, err
		}
		account, _, _ := strings.Cut(u.Host, ".")
		credential, err := sdkazblob.NewSharedKeyCredential(account, key)
		if err != nil {
			return nil, err
		}
		return sdkazblob.NewClientWithSharedKeyCredential(serviceURL, credential, nil)
	}
	if sas := strings.TrimPrefix(getenv("ARM_SAS_TOKEN"), "?"); sas != "" {
		return sdkazblob.NewClientWithNoCredential(serviceURL+"?"+sas, nil)
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
	return sdkazblob.NewClient(serviceURL, credential, nil)
}

// List returns the names of the blobs under prefix in container.
func (s *Store) List(ctx context.Context, container, prefix string) ([]string, error) {
	var names []string
	pages := s.client.NewListBlobsFlatPager(container, &sdkazblob.ListBlobsFlatOptions{Prefix: &prefix})
	for pages.More() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			// A transport error quotes the URL, and with it a SAS token.
			return nil, managed.WithoutURL(err)
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
func (s *Store) Get(ctx context.Context, container, name string) ([]byte, error) {
	resp, err := s.client.DownloadStream(ctx, container, name, nil)
	if err != nil {
		return nil, managed.WithoutURL(err)
	}
	defer resp.Body.Close()
	return managed.ReadState(resp.Body)
}
