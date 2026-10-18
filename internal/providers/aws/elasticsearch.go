// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticsearchservice"
	"github.com/aws/aws-sdk-go-v2/service/elasticsearchservice/types"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// EsLister lists the region's Elasticsearch Service domains, which are
// the OpenSearch Service domains of either engine.
type EsLister struct {
	Service
}

// List lists every domain, imported by its name and recording it
// as domain_name: an Elasticsearch domain as an aws_elasticsearch_domain,
// and an OpenSearch one as an aws_opensearch_domain, the type Terraform
// manages OpenSearch domains with. A domain whose engine the answer
// doesn't name is taken for an Elasticsearch one. ListDomainNames answers
// with all the domains at once.
func (g *EsLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := elasticsearchservice.NewFromConfig(config)
	out, err := client.ListDomainNames(g.Context(), &elasticsearchservice.ListDomainNamesInput{})
	if err != nil {
		return err
	}
	for _, domain := range out.DomainNames {
		name := aws.ToString(domain.DomainName)
		resourceType := "aws_elasticsearch_domain"
		if domain.EngineType == types.EngineTypeOpenSearch {
			resourceType = "aws_opensearch_domain"
		}
		g.Resources = append(g.Resources, harvest.Record(name, name, resourceType, "aws",
			map[string]string{"domain_name": name}))
	}
	return nil
}
