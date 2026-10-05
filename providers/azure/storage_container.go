package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage/v4"
	"github.com/IgnatG/infraharvest/terraformutils"
)

const (
	containerIDFormat = "https://%s.blob.core.windows.net/%s"
)

type StorageContainerGenerator struct {
	AzureService
}

// blobContainer is a blob container with the storage account and resource
// group it belongs to.
type blobContainer struct {
	accountName   string
	resourceGroup string
	name          string
}

func NewStorageContainerGenerator(subscriptionID string, credential azcore.TokenCredential, options *arm.ClientOptions, rg string) *StorageContainerGenerator {
	storageContainerGenerator := new(StorageContainerGenerator)
	storageContainerGenerator.Args = map[string]interface{}{
		"subscription_id": subscriptionID,
		"credential":      credential,
		"client_options":  options,
		"resource_group":  rg,
	}

	return storageContainerGenerator
}

// listBlobContainers lists the blob containers of every storage account in
// scope (the resource group, or the whole subscription).
func (g StorageContainerGenerator) listBlobContainers(ctx context.Context) ([]blobContainer, error) {
	var containers []blobContainer
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	accountsClient, err := armstorage.NewAccountsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	blobContainersClient, err := armstorage.NewBlobContainersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	accounts, err := listStorageAccounts(ctx, accountsClient, resourceGroup)
	if err != nil {
		return nil, err
	}

	for _, storageAccount := range accounts {
		parsedStorageAccountResourceID, err := ParseAzureResourceID(*storageAccount.ID)
		if err != nil {
			break
		}
		items, err := listAll(ctx,
			blobContainersClient.NewListPager(parsedStorageAccountResourceID.ResourceGroup, *storageAccount.Name, nil),
			func(p armstorage.BlobContainersClientListResponse) []*armstorage.ListContainerItem { return p.Value })
		for _, containerItem := range items {
			containers = append(containers, blobContainer{
				accountName:   *storageAccount.Name,
				resourceGroup: parsedStorageAccountResourceID.ResourceGroup,
				name:          *containerItem.Name,
			})
		}
		if err != nil {
			return containers, err
		}
	}

	return containers, nil
}

func containerResource(container blobContainer) terraformutils.Resource {
	return terraformutils.NewResource(
		fmt.Sprintf(containerIDFormat, container.accountName, container.name),
		container.name,
		"azurerm_storage_container",
		"azurerm",
		map[string]string{
			"storage_account_name": container.accountName,
			"name":                 container.name,
		})
}

func (g StorageContainerGenerator) ListBlobContainers() ([]terraformutils.Resource, error) {
	var containerResources []terraformutils.Resource
	containers, err := g.listBlobContainers(context.Background())
	for _, container := range containers {
		containerResources = append(containerResources, containerResource(container))
	}
	return containerResources, err
}

func (g *StorageContainerGenerator) InitResources() error {
	storageAccounts, err := g.ListBlobContainers()
	if err != nil {
		return err
	}

	g.Resources = storageAccounts

	return nil
}
