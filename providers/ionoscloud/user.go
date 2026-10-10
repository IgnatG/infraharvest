package ionoscloud

import (
	"log"

	"github.com/IgnatG/infraharvest/providers/ionoscloud/helpers"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type UserGenerator struct {
	Service
}

func (g *UserGenerator) InitResources() error {
	client := g.generateClient()
	cloudAPIClient := client.CloudAPIClient
	resourceType := "ionoscloud_user"

	usersResponse, _, err := cloudAPIClient.UserManagementApi.UmUsersGet(g.Context()).Execute()
	if err != nil {
		return err
	}
	if usersResponse.Items == nil {
		log.Printf("[WARNING] expected a response containing users but received 'nil' instead")
		return nil
	}
	for _, user := range *usersResponse.Items {
		g.Resources = append(g.Resources, terraformutils.NewResource(
			*user.Id,
			*user.Id,
			resourceType,
			helpers.Ionos,
			map[string]string{}))
	}
	return nil
}
