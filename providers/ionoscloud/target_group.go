package ionoscloud

import (
	"log"

	"github.com/IgnatG/infraharvest/providers/ionoscloud/helpers"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type TargetGroupGenerator struct {
	Service
}

func (g *TargetGroupGenerator) InitResources() error {
	client := g.generateClient()
	cloudAPIClient := client.CloudAPIClient
	resourceType := "ionoscloud_target_group"

	targetGroupResponse, _, err := cloudAPIClient.TargetGroupsApi.TargetgroupsGet(g.Context()).Depth(1).Execute()
	if err != nil {
		return err
	}
	if targetGroupResponse.Items == nil {
		log.Printf("[WARNING] expected a response containing target groups but received 'nil' instead.")
		return nil
	}
	targetGroups := *targetGroupResponse.Items
	for _, targetGroup := range targetGroups {
		if targetGroup.Properties == nil || targetGroup.Properties.Name == nil {
			log.Printf(
				"[WARNING] 'nil' values in the response for target group with ID %v, skipping this resource.\n",
				*targetGroup.Id)
			continue
		}
		g.Resources = append(g.Resources, terraformutils.NewResource(
			*targetGroup.Id,
			*targetGroup.Properties.Name+"-"+*targetGroup.Id,
			resourceType,
			helpers.Ionos,
			map[string]string{}))
	}
	return nil
}
