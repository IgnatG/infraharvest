// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"

	"github.com/infraharvest/infraharvest-cli/internal/catalogue"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
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

// tagFilterLimit is the most resource type filters one GetResources call
// takes.
const tagFilterLimit = 100

// tagServices returns the ARN services, sorted, of the resources the tag
// index can find: those whose ID, import ID or arn attribute is an ARN, by
// the ARN's service, and those whose type cat gives an arn entry, by its
// service. The tags of other resources can't be found, so they need none,
// and neither do IAM's, which the Tagging API doesn't list.
func tagServices(cat *catalogue.Catalogue, resources []harvest.Resource) []string {
	seen := map[string]bool{}
	for _, r := range resources {
		id, _ := importID(cat, r)
		for _, c := range []string{r.Observed.ID, id, r.Observed.Attributes["arn"]} {
			if parts := strings.SplitN(c, ":", 6); len(parts) == 6 && parts[0] == "arn" && parts[2] != "" {
				seen[parts[2]] = true
			}
		}
		if key, ok := arnResourceOf(cat, r.Address.Type); ok {
			seen[key.service] = true
		}
	}
	delete(seen, "iam")
	return slices.Sorted(maps.Keys(seen))
}

// Tags reads the tags of the listed resources from the Resource Groups
// Tagging API of the region they were listed in: us-east-1 for the global
// services, where it lists CloudFront distributions and Route 53 zones.
// It asks only for the services of the listed resources whose tags it can
// find (see tagServices), at most tagFilterLimit to a call, and not at all
// when there are none; should that fail, as when the API refuses a filter,
// it asks for every tagged resource instead. It returns them by "type id".
func (p *Provider) Tags(ctx context.Context, resources []harvest.Resource) (map[string]map[string]string, error) {
	cat := p.catalogue()
	services := tagServices(cat, resources)
	if len(services) == 0 {
		return map[string]map[string]string{}, nil
	}
	config, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	if p.region == GlobalPseudoRegion {
		config = config.Copy()
		config.Region = "us-east-1"
	}
	client := resourcegroupstaggingapi.NewFromConfig(config)
	index := newTagIndex(cat)
	var readErr error
	for batch := range slices.Chunk(services, tagFilterLimit) {
		if readErr = readTagPages(ctx, client, batch, index); readErr != nil {
			break
		}
	}
	if readErr != nil {
		// An API, or an emulator, that refuses a filter answers with an
		// error of its own choosing, so any error is worth one more try.
		log.Printf("aws: tags in %s: reading by service filters failed, so every tagged resource is read: %v", config.Region, readErr)
		index = newTagIndex(cat)
		readErr = readTagPages(ctx, client, nil, index)
	}
	if readErr != nil {
		return nil, fmt.Errorf("tags in %s: %w", config.Region, readErr)
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

// readTagPages adds to index the tagged resources of the services in
// filters, every page of them, or of every service when filters is empty.
func readTagPages(ctx context.Context, client *resourcegroupstaggingapi.Client, filters []string, index *tagIndex) error {
	input := &resourcegroupstaggingapi.GetResourcesInput{ResourcesPerPage: aws.Int32(100)}
	if len(filters) > 0 {
		input.ResourceTypeFilters = filters
	}
	pages := resourcegroupstaggingapi.NewGetResourcesPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, m := range page.ResourceTagMappingList {
			tags := make(map[string]string, len(m.Tags))
			for _, t := range m.Tags {
				tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
			}
			index.add(aws.ToString(m.ResourceARN), tags)
		}
	}
	return nil
}
