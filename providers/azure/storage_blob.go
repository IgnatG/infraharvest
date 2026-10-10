package azure

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/IgnatG/infraharvest/terraformutils"
)

const (
	blobFormatString = `https://%s.blob.core.windows.net`
	blobIDFormat     = `https://%s.blob.core.windows.net/%s/%s`
)

type StorageBlobGenerator struct {
	AzureService
}

func (g StorageBlobGenerator) getAccountPrimaryKey(ctx context.Context, client *armstorage.AccountsClient, accountName, accountGroupName string) (string, error) {
	response, err := client.ListKeys(ctx, accountGroupName, accountName, &armstorage.AccountsClientListKeysOptions{Expand: to.Ptr("kerb")})
	if err != nil {
		return "", fmt.Errorf("failed to list keys: %w", err)
	}
	if len(response.Keys) == 0 || response.Keys[0] == nil || response.Keys[0].Value == nil {
		return "", fmt.Errorf("storage account %s has no access key", accountName)
	}
	return *response.Keys[0].Value, nil
}

func (g StorageBlobGenerator) getBlobsFromContainer(ctx context.Context, blobClient *azblob.Client, containerName string) ([]*container.BlobItem, error) {
	return listAll(ctx,
		blobClient.NewListBlobsFlatPager(containerName, &azblob.ListBlobsFlatOptions{
			Include: azblob.ListBlobsInclude{Snapshots: true},
		}),
		func(p azblob.ListBlobsFlatResponse) []*container.BlobItem {
			if p.Segment == nil {
				return nil
			}
			return p.Segment.BlobItems
		})
}

// newBlobClient returns a blob service client for the storage account,
// signed in with its primary access key.
func (g StorageBlobGenerator) newBlobClient(ctx context.Context, accountsClient *armstorage.AccountsClient, accountName, accountGroupName string) (*azblob.Client, error) {
	accountPrimaryKey, err := g.getAccountPrimaryKey(ctx, accountsClient, accountName, accountGroupName)
	if err != nil {
		return nil, err
	}
	sharedKeyCredential, err := azblob.NewSharedKeyCredential(accountName, accountPrimaryKey)
	if err != nil {
		return nil, err
	}
	return azblob.NewClientWithSharedKeyCredential(fmt.Sprintf(blobFormatString, accountName), sharedKeyCredential, nil)
}

func (g StorageBlobGenerator) listStorageBlobs() ([]terraformutils.Resource, error) {
	var storageBlobsResources []terraformutils.Resource
	ctx := g.Context()

	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	blobContainerGenerator := NewStorageContainerGenerator(subscriptionID, credential, options, resourceGroup)
	blobContainers, err := blobContainerGenerator.listBlobContainers(ctx)
	if err != nil {
		return storageBlobsResources, err
	}
	accountsClient, err := armstorage.NewAccountsClient(subscriptionID, credential, options)
	if err != nil {
		return storageBlobsResources, err
	}

	blobClients := map[string]*azblob.Client{}
	for _, c := range blobContainers {
		blobClient, ok := blobClients[c.accountName]
		if !ok {
			blobClient, err = g.newBlobClient(ctx, accountsClient, c.accountName, c.resourceGroup)
			if err != nil {
				return storageBlobsResources, err
			}
			blobClients[c.accountName] = blobClient
		}

		blobsList, err := g.getBlobsFromContainer(ctx, blobClient, c.name)
		if err != nil {
			return storageBlobsResources, err
		}

		for _, blobItem := range blobsList {
			if blobItem.Name == nil {
				return storageBlobsResources, errors.New("listed a blob without a name")
			}
			storageBlobsResources = append(storageBlobsResources, terraformutils.NewSimpleResource(
				fmt.Sprintf(blobIDFormat, c.accountName, c.name, *blobItem.Name),
				*blobItem.Name,
				"azurerm_storage_blob",
				"azurerm"))
		}
	}

	return storageBlobsResources, nil
}

func (g *StorageBlobGenerator) InitResources() error {
	resources, err := g.listStorageBlobs()
	if err != nil {
		return err
	}

	g.Resources = append(g.Resources, resources...)

	return nil
}
