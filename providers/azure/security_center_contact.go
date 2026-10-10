package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/security/armsecurity"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type SecurityCenterContactGenerator struct {
	AzureService
}

func (g SecurityCenterContactGenerator) listContacts() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()

	// Security contacts belong to the subscription, not to a resource group.
	if resourceGroup != "" {
		return resources, nil
	}
	securityCenterContactClient, err := armsecurity.NewContactsClient(subscriptionID, credential, options)
	if err != nil {
		return resources, err
	}
	contacts, err := listAll(ctx, securityCenterContactClient.NewListPager(nil),
		func(p armsecurity.ContactsClientListResponse) []*armsecurity.Contact { return p.Value })
	for _, contact := range contacts {
		resources = append(resources, terraformutils.NewSimpleResource(
			*contact.ID,
			*contact.Name,
			"azurerm_security_center_contact",
			g.ProviderName))
	}
	if err != nil {
		return resources, err
	}

	return resources, nil
}

func (g *SecurityCenterContactGenerator) InitResources() error {
	resources, err := g.listContacts()
	if err != nil {
		return err
	}

	g.Resources = append(g.Resources, resources...)

	return nil
}
