package ionoscloud

import (
	"log"

	"github.com/IgnatG/infraharvest/providers/ionoscloud/helpers"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type ContainerRegistryTokenGenerator struct {
	Service
}

func (g *ContainerRegistryTokenGenerator) InitResources() error {
	client := g.generateClient()
	crClient := client.ContainerRegistryAPIClient
	resourceType := "ionoscloud_container_registry_token"

	registriesResponse, _, err := crClient.RegistriesApi.RegistriesGet(g.Context()).Execute()
	if err != nil {
		return err
	}
	if registriesResponse.Items == nil {
		log.Printf("[WARNING] expected a response containing registries but received 'nil' instead")
		return nil
	}
	registries := *registriesResponse.Items
	for _, registry := range registries {
		tokensResponse, _, err := crClient.TokensApi.RegistriesTokensGet(g.Context(), *registry.Id).Execute()
		if err != nil {
			return err
		}
		if tokensResponse.Items == nil {
			log.Printf("[WARNING] expected a response containing container registry tokens, but received 'nil' instead")
			return nil
		}
		crTokens := *tokensResponse.Items
		for _, crToken := range crTokens {
			if crToken.Properties == nil || crToken.Properties.Name == nil {
				log.Printf("[WARNING] 'nil' values in the response for the container registry token with ID: %v, skipping this resource", *crToken.Id)
				continue
			}
			g.Resources = append(g.Resources, terraformutils.NewResource(
				*crToken.Id,
				*crToken.Properties.Name+"-"+*crToken.Id,
				resourceType,
				helpers.Ionos,
				map[string]string{"registry_id": *registry.Id}))
		}
	}
	return nil
}
