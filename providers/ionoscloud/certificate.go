package ionoscloud

import (
	"log"

	"github.com/IgnatG/infraharvest/providers/ionoscloud/helpers"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type CertificateGenerator struct {
	Service
}

func (g *CertificateGenerator) InitResources() error {
	client := g.generateClient()
	certManagerAPIClient := client.CertificateManagerAPIClient
	resourceType := "ionoscloud_certificate"

	response, _, err := certManagerAPIClient.CertificatesApi.CertificatesGet(g.Context()).Execute()
	if err != nil {
		return err
	}
	if response.Items == nil {
		log.Printf("[WARNING] expected a response containing certificates but received 'nil' instead.")
		return nil
	}
	certificates := *response.Items
	for _, certificate := range certificates {
		if certificate.Properties == nil || certificate.Properties.Name == nil {
			log.Printf("[WARNING] 'nil' values in the response for the certificate with ID %v, skipping this resource.", *certificate.Id)
			continue
		}
		g.Resources = append(g.Resources, terraformutils.NewResource(
			*certificate.Id,
			*certificate.Properties.Name+"-"+*certificate.Id,
			resourceType,
			helpers.Ionos,
			map[string]string{}))
	}
	return nil
}
