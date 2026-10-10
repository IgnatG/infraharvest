package ionoscloud

import (
	"log"

	"github.com/IgnatG/infraharvest/providers/ionoscloud/helpers"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type DBaaSMongoUserGenerator struct {
	Service
}

func (g *DBaaSMongoUserGenerator) InitResources() error {
	client := g.generateClient()
	dbaasMongoClient := client.DBaaSMongoAPIClient
	resourceType := "ionoscloud_mongo_user"

	response, _, err := dbaasMongoClient.ClustersApi.ClustersGet(g.Context()).Execute()
	if err != nil {
		return err
	}
	if response.Items == nil {
		log.Printf("[WARNING] expected a response containing Mongo DB clusters but received 'nil' instead")
	}
	clusters := *response.Items
	for _, cluster := range clusters {
		usersResponse, _, err := dbaasMongoClient.UsersApi.ClustersUsersGet(g.Context(), *cluster.Id).Execute()
		if err != nil {
			return err
		}
		if usersResponse.Items == nil {
			log.Printf("[WARNING] expected a response containing Mongo users but received 'nil' instead, skipping search for Mongo cluster with ID: %v", *cluster.Id)
			continue
		}
		users := *usersResponse.Items
		for _, user := range users {
			if user.Properties == nil || user.Properties.Username == nil {
				log.Printf("[WARNING] 'nil' values in the response for Mongo user, skipping this resource")
				continue
			}
			userID := *cluster.Id + *user.Properties.Username
			g.Resources = append(g.Resources, terraformutils.NewResource(
				userID,
				userID,
				resourceType,
				helpers.Ionos,
				map[string]string{helpers.ClusterID: *cluster.Id, helpers.UsernameArg: *user.Properties.Username}))
		}
	}
	return nil
}
