package ionoscloud

import (
	"log"

	"github.com/IgnatG/infraharvest/providers/ionoscloud/helpers"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type ShareGenerator struct {
	Service
}

func (g *ShareGenerator) InitResources() error {
	client := g.generateClient()
	cloudAPIClient := client.CloudAPIClient
	resourceType := "ionoscloud_share"

	groups, _, err := cloudAPIClient.UserManagementApi.UmGroupsGet(g.Context()).Depth(1).Execute()
	if err != nil {
		return err
	}
	if groups.Items == nil {
		log.Printf("[WARNING] expected a response containing groups but received 'nil' instead.")
		return nil
	}
	for _, group := range *groups.Items {
		shares, _, err := cloudAPIClient.UserManagementApi.UmGroupsSharesGet(g.Context(), *group.Id).Execute()
		if err != nil {
			return err
		}
		if shares.Items == nil {
			log.Printf("[WARNING] expected a response containing shares but received 'nil' instead, skipping search for group with ID: %s", *group.Id)
			continue
		}
		for _, share := range *shares.Items {
			g.Resources = append(g.Resources, terraformutils.NewResource(
				*share.Id,
				*share.Id,
				resourceType,
				helpers.Ionos,
				map[string]string{helpers.GroupID: *group.Id, helpers.ResourceID: *share.Id}))
		}
	}
	return nil
}
