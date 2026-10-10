package honeycombio

import (
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type TriggerGenerator struct {
	HoneycombService
}

func (g *TriggerGenerator) InitResources() error {
	client, err := g.newClient()
	if err != nil {
		return fmt.Errorf("unable to initialize Honeycomb client: %v", err)
	}

	for _, dataset := range g.datasets {
		if dataset.Slug == environmentWideDatasetSlug {
			// environment-wide Triggers are not supported
			continue
		}
		triggers, err := client.Triggers.List(g.Context(), dataset.Slug)
		if err != nil {
			return fmt.Errorf("unable to list Honeycomb triggers for dataset %s: %v", dataset.Slug, err)
		}

		for _, trigger := range triggers {
			g.Resources = append(g.Resources, terraformutils.NewResource(
				trigger.ID,
				trigger.ID,
				"honeycombio_trigger",
				"honeycombio",
				map[string]string{
					"dataset": dataset.Name,
				}))
		}
	}

	return nil
}
