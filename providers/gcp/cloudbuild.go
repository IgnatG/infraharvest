package gcp

import (
	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	pb "cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"google.golang.org/api/iterator"

	"github.com/IgnatG/infraharvest/terraformutils"
)

const cbMaxPageSize = 50

type CloudBuildGenerator struct {
	GCPService
}

// InitResources generates TerraformResources from GCP API.
func (g *CloudBuildGenerator) InitResources() error {
	ctx := g.Context()

	c, err := cloudbuild.NewClient(ctx, grpcClientOptions()...)
	if err != nil {
		return err
	}

	var triggers []*pb.BuildTrigger
	it := c.ListBuildTriggers(ctx, &pb.ListBuildTriggersRequest{
		ProjectId: g.GetArgs()["project"].(string),
		PageSize:  cbMaxPageSize,
	})
	for {
		trigger, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		triggers = append(triggers, trigger)
	}

	g.Resources = g.createBuildTriggers(triggers)
	return nil
}

func (g *CloudBuildGenerator) createBuildTriggers(triggers []*pb.BuildTrigger) []terraformutils.Resource {
	var resources []terraformutils.Resource

	for _, trigger := range triggers {
		resources = append(resources, terraformutils.NewResource(
			trigger.GetId(),
			trigger.GetName(),
			"google_cloudbuild_trigger",
			g.ProviderName,
			map[string]string{
				"project": g.GetArgs()["project"].(string),
			}))
	}

	return resources
}
