// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// arnResource is where the ARNs of a Terraform type's resources name them:
// the ARN's service, and the resource type its resource part starts with,
// "" if the part is the name alone (an S3 bucket's).
type arnResource struct {
	service, resourceType string
}

// arnResourceOf returns where the ARNs of a taggable Terraform type whose
// IDs are names, not ARNs, name its resources, from the catalogue cat.
// Resources of the other types are tagged only if their ID, import ID or
// arn attribute is an ARN the Resource Groups Tagging API lists.
func arnResourceOf(cat *catalogue.Catalogue, typ string) (arnResource, bool) {
	e, ok := cat.Entry(typ)
	if !ok || e.ARN == nil {
		return arnResource{}, false
	}
	return arnResource{e.ARN.Service, e.ARN.ResourceType}, true
}

// tagIndex finds the tags of resources by ARN, or by the service, resource
// type and name an ARN is made of, which the catalogue cat gives types.
type tagIndex struct {
	byARN map[string]map[string]string
	// byName holds the ARNs of each service, resource type and name; a
	// name more than one ARN shares can't say which is meant.
	byName map[arnResource]map[string][]string
	cat    *catalogue.Catalogue
}

func newTagIndex(cat *catalogue.Catalogue) *tagIndex {
	return &tagIndex{byARN: map[string]map[string]string{}, byName: map[arnResource]map[string][]string{}, cat: cat}
}

// add records the tags of the resource arn names.
func (x *tagIndex) add(arn string, tags map[string]string) {
	x.byARN[arn] = tags
	// arn:partition:service:region:account:resource
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 {
		return
	}
	service, resource := parts[2], parts[5]
	resourceType, name := splitARNResource(resource)
	key := arnResource{service, resourceType}
	if x.byName[key] == nil {
		x.byName[key] = map[string][]string{}
	}
	names := []string{name}
	// A role or user path, an Auto Scaling group's ID, an API Gateway
	// stage: the name is the last part.
	if i := strings.LastIndexAny(name, "/:"); i >= 0 && i < len(name)-1 {
		names = append(names, name[i+1:])
	}
	for _, n := range names {
		if !slices.Contains(x.byName[key][n], arn) {
			x.byName[key][n] = append(x.byName[key][n], arn)
		}
	}
}

// splitARNResource splits an ARN's resource part into its resource type
// and name: role/admin, function:name, /restapis/id, or a bucket name
// alone.
func splitARNResource(resource string) (resourceType, name string) {
	trimmed := strings.TrimPrefix(resource, "/")
	i := strings.IndexAny(trimmed, "/:")
	if i < 0 {
		return "", resource
	}
	return trimmed[:i], trimmed[i+1:]
}

// lookup returns the tags of r, whose import ID is importID.
func (x *tagIndex) lookup(r harvest.Resource, importID string) (map[string]string, bool) {
	candidates := []string{r.Observed.ID, importID, r.Observed.Attributes["arn"]}
	for _, c := range candidates {
		if tags, ok := x.byARN[c]; ok && c != "" {
			return tags, true
		}
	}
	key, ok := arnResourceOf(x.cat, r.Address.Type)
	if !ok {
		return nil, false
	}
	for _, c := range candidates[:2] {
		if c == "" {
			continue
		}
		names := []string{c, strings.TrimPrefix(c, "/")}
		// A queue's ID is its URL, which ends with its name.
		if strings.Contains(c, "://") {
			names = append(names, c[strings.LastIndex(c, "/")+1:])
		}
		for _, n := range names {
			if arns := x.byName[key][n]; len(arns) == 1 {
				return x.byARN[arns[0]], true
			}
		}
	}
	return nil, false
}

// Tags reads the tags of the listed resources from the Resource Groups
// Tagging API of the region they were listed in: us-east-1 for the global
// services, where it lists CloudFront distributions and Route 53 zones.
// It returns them by "type id".
func (p *Provider) Tags(ctx context.Context, resources []harvest.Resource) (map[string]map[string]string, error) {
	config, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	if p.region == GlobalPseudoRegion {
		config = config.Copy()
		config.Region = "us-east-1"
	}
	cat := p.catalogue()
	index := newTagIndex(cat)
	pages := resourcegroupstaggingapi.NewGetResourcesPaginator(resourcegroupstaggingapi.NewFromConfig(config), &resourcegroupstaggingapi.GetResourcesInput{
		ResourcesPerPage: aws.Int32(100),
	}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("tags in %s: %w", config.Region, err)
		}
		for _, m := range page.ResourceTagMappingList {
			tags := make(map[string]string, len(m.Tags))
			for _, t := range m.Tags {
				tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
			}
			index.add(aws.ToString(m.ResourceARN), tags)
		}
	}
	found := map[string]map[string]string{}
	for _, r := range resources {
		id, _ := importID(cat, r)
		if tags, ok := index.lookup(r, id); ok && len(tags) > 0 {
			found[r.Address.Type+" "+r.Observed.ID] = tags
		}
	}
	log.Printf("aws: tags in %s: the Tagging API lists %d tagged resources, %d of the %d listed matched", config.Region, len(index.byARN), len(found), len(resources))
	return found, nil
}
