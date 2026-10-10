package honeycombio

import (
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type QueryGenerator struct {
	HoneycombService
}

func (g *QueryGenerator) InitResources() error {
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
				trigger.QueryID,
				trigger.QueryID,
				"honeycombio_query",
				"honeycombio",
				map[string]string{
					"dataset": dataset.Name,
				}))
		}
	}

	boards, err := client.Boards.List(g.Context())
	if err != nil {
		return fmt.Errorf("unable to list Honeycomb boards: %v", err)
	}

	for _, board := range boards {
		for _, query := range board.Queries {
			if query.Dataset == "" {
				// assume an unset dataset is an environment-wide query
				query.Dataset = environmentWideDatasetSlug
			}
			if _, exists := g.datasets[query.Dataset]; exists {
				g.Resources = append(g.Resources, terraformutils.NewResource(
					query.QueryID,
					query.QueryID,
					"honeycombio_query",
					"honeycombio",
					map[string]string{
						"dataset": query.Dataset,
					}))
			}
		}
	}

	return nil
}
