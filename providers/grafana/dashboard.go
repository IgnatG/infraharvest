package grafana

import (
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"
	gapi "github.com/grafana/grafana-api-golang-client"
)

type DashboardGenerator struct {
	GrafanaService
}

func (g *DashboardGenerator) InitResources() error {
	client, err := g.buildClient()
	if err != nil {
		return fmt.Errorf("unable to build grafana client: %v", err)
	}

	err = g.createDashboardResources(client)
	if err != nil {
		return err
	}

	return nil
}

func (g *DashboardGenerator) createDashboardResources(client *gapi.Client) error {
	dashboards, err := client.Dashboards()
	if err != nil {
		return fmt.Errorf("unable to list grafana dashboards: %v", err)
	}

	for _, dashboard := range dashboards {
		resource := terraformutils.NewResource(
			dashboard.UID,
			dashboard.Title,
			"grafana_dashboard",
			"grafana",
			map[string]string{})

		g.Resources = append(g.Resources, resource)
	}

	return nil
}
