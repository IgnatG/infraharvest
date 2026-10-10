package ionoscloud

import (
	"log"

	"github.com/IgnatG/infraharvest/providers/ionoscloud/helpers"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type GroupGenerator struct {
	Service
}

func (g *GroupGenerator) InitResources() error {
	client := g.generateClient()
	cloudAPIClient := client.CloudAPIClient
	resourceType := "ionoscloud_group"

	groupResponse, _, err := cloudAPIClient.UserManagementApi.UmGroupsGet(g.Context()).Depth(1).Execute()
	if err != nil {
		return err
	}
	if groupResponse.Items == nil {
		log.Printf("[WARNING] expected a response containing groups but received 'nil' instead.")
		return nil
	}
	groups := *groupResponse.Items
	for _, group := range groups {
		if group.Properties == nil || group.Properties.Name == nil {
			log.Printf(
				"[WARNING] 'nil' values in the response for group with ID %v, skipping this resource.\n",
				*group.Id)
			continue
		}
		g.Resources = append(g.Resources, terraformutils.NewResource(
			*group.Id,
			*group.Properties.Name+"-"+*group.Id,
			resourceType,
			helpers.Ionos,
			map[string]string{}))
	}
	return nil
}
