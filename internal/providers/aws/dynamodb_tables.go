// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// legacyGlobalTableVersion is the version of the global tables of 2017,
// whose tables are separate tables in each region, which Terraform imports
// one by one, joined by an aws_dynamodb_global_table.
const legacyGlobalTableVersion = "2017.11.29"

// DynamoDbLister lists the region's DynamoDB tables (the `dynamodb`
// service).
type DynamoDbLister struct {
	Service
}

// List records every table as an aws_dynamodb_table, imported by its
// name. Of each table it asks DescribeTable which regions hold replicas of
// it, as a global table (version 2019.11.21) has, and records them as
// replica.#, and replica.N.region_name in the order of their names, so
// that the default selection imports the global table once (see
// excludeGlobalTableReplicas). The tables of a page are described
// resourceParallel at a time, and recorded in the page's order. A listing
// error fails the service; a table that can't be described is recorded
// without its replicas, and fails it in part.
func (g *DynamoDbLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := dynamodb.NewFromConfig(config)
	var errs []error
	pages := dynamodb.NewListTablesPaginator(svc, &dynamodb.ListTablesInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		tables := make([]*harvest.Resource, len(page.TableNames))
		tableErrs := make([]error, len(page.TableNames))
		inParallel(len(page.TableNames), resourceParallel, func(i int) {
			tables[i], tableErrs[i] = g.table(svc, page.TableNames[i])
		})
		for i, table := range tables {
			if table != nil {
				g.Resources = append(g.Resources, *table)
			}
			errs = append(errs, tableErrs[i])
		}
	}
	return harvest.Partial(errors.Join(errs...))
}

// table returns the table name, with the regions of its replicas (see
// replicaAttributes), or nil when it was deleted since it was listed. A
// table that can't be described is returned without its replicas, with
// the error.
func (g *DynamoDbLister) table(svc *dynamodb.Client, name string) (*harvest.Resource, error) {
	out, err := svc.DescribeTable(g.Context(), &dynamodb.DescribeTableInput{TableName: aws.String(name)})
	var gone *types.ResourceNotFoundException
	if errors.As(err, &gone) {
		// Deleted since it was listed.
		return nil, nil
	}
	r := harvest.RecordBare(name, name, "aws_dynamodb_table", "aws")
	if err != nil {
		return &r, fmt.Errorf("table %s: %w", name, err)
	}
	replicaAttributes(out.Table, r.Observed.Attributes)
	return &r, nil
}

// replicaAttributes adds to attributes the regions of the replicas of
// table, a global table of version 2019.11.21, that aren't being deleted,
// as replica.# and replica.N.region_name, in the order of their names. A
// table of no global table, or of one of 2017, gets none.
func replicaAttributes(table *types.TableDescription, attributes map[string]string) {
	if table == nil || aws.ToString(table.GlobalTableVersion) == legacyGlobalTableVersion {
		return
	}
	var regions []string
	for _, r := range table.Replicas {
		if region := aws.ToString(r.RegionName); region != "" && r.ReplicaStatus != types.ReplicaStatusDeleting {
			regions = append(regions, region)
		}
	}
	if len(regions) == 0 {
		return
	}
	slices.Sort(regions)
	attributes["replica.#"] = strconv.Itoa(len(regions))
	for i, region := range regions {
		attributes["replica."+strconv.Itoa(i)+".region_name"] = region
	}
}

// globalTableHome returns the region the default selection imports the
// global table of r, a DynamoDB table listed in region, in: of region and
// those of the replicas r records (see replicaAttributes), the first by
// name. AWS doesn't say in which region a global table was created, nor
// does any of its replicas lead, so the rule only has to give every
// region the same answer. A table that records no replicas is at home in
// its own region.
func globalTableHome(r harvest.Resource, region string) string {
	home := region
	count, _ := strconv.Atoi(r.Observed.Attributes["replica.#"])
	for i := range count {
		if replica := r.Observed.Attributes["replica."+strconv.Itoa(i)+".region_name"]; replica != "" && replica < home {
			home = replica
		}
	}
	return home
}
