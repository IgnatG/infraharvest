// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// tagFieldPrefix starts the field path of a filter on a tag, as --tag
// env=prod writes tags.env.
const tagFieldPrefix = "tags."

// ec2TagFilters turns the filters on tags (--tag, or --where tags.<key>)
// that concern one of serviceNames into EC2 API filters, so that EC2 lists
// only the matching resources instead of all of them. A filter on
// tags.<key> becomes the EC2 filter tag:<key> with the filter's values, in
// the order the flags were given; a filter on the key alone becomes
// tag-key with the key.
//
// serviceNames are matched against the filter's Service exactly: the names
// a filter may name, such as the type without aws_ (instance) and the name
// of the service (ec2_instance); a filter that names none, as --tag and
// --where write them, concerns every service. Filters on IDs
// or on other attributes are not passed on; harvest applies ID filters
// itself after listing. It returns nil when no filter applies.
func ec2TagFilters(filters []harvest.Filter, serviceNames ...string) []types.Filter {
	var tagFilters []types.Filter
	for i := range filters {
		filter := &filters[i]
		key, ok := strings.CutPrefix(filter.Path, tagFieldPrefix)
		if !ok || !slices.ContainsFunc(serviceNames, filter.AppliesTo) {
			continue
		}
		if filter.Values == nil {
			// Only the key: EC2's tag-key filter, whatever the value.
			tagFilters = append(tagFilters, types.Filter{Name: aws.String("tag-key"), Values: []string{key}})
			continue
		}
		tagFilters = append(tagFilters, types.Filter{
			Name:   aws.String("tag:" + key),
			Values: filter.Values,
		})
	}
	return tagFilters
}
