package honeycombio

import (
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type DerivedColumnGenerator struct {
	HoneycombService
}

func (g *DerivedColumnGenerator) InitResources() error {
	client, err := g.newClient()
	if err != nil {
		return fmt.Errorf("unable to initialize Honeycomb client: %v", err)
	}

	for _, dataset := range g.datasets {
		columns, err := client.DerivedColumns.List(g.Context(), dataset.Slug)
		if err != nil {
			return fmt.Errorf("unable to list Honeycomb derived columns for dataset %q: %v", dataset.Slug, err)
		}

		for _, column := range columns {
			g.Resources = append(g.Resources, terraformutils.NewResource(
				column.ID,
				fmt.Sprintf("%s_%s", dataset.Name, column.Alias),
				"honeycombio_derived_column",
				"honeycombio",
				map[string]string{
					"dataset": dataset.Name,
					"alias":   column.Alias,

					"expression": column.Expression,
				}))
		}
	}

	return nil
}
