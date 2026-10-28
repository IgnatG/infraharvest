// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// otherEngines are the engines of services that share the RDS API but
// have Terraform types of their own: the RDS API lists DocumentDB's and
// Neptune's clusters and instances with its own, and the RDS types must
// leave them out.
var otherEngines = []string{"docdb", "neptune"}

// otherEngineFamily reports whether a parameter group family, such as
// neptune1.3, is one of otherEngines'.
func otherEngineFamily(family string) bool {
	return slices.ContainsFunc(otherEngines, func(engine string) bool { return strings.HasPrefix(family, engine) })
}

// manualSnapshots is the snapshot type of the snapshots people take. The
// automated ones belong to their database and expire with its retention
// period, so only manual snapshots are listed.
const manualSnapshots = "manual"

// RDSLister lists what the RDS types of a region manage: clusters and
// their instances, instances, manual snapshots, proxies, parameter, subnet
// and option groups, event subscriptions and global clusters, each by the
// name Terraform imports it with.
type RDSLister struct {
	Service
	// docdbSubnetGroups are the subnet groups DocumentDB clusters use, which
	// listDBClusters finds: the docdb service lists them as
	// aws_docdb_subnet_group, so listDBSubnetGroups leaves them out.
	docdbSubnetGroups map[string]bool
}

// List lists each kind in turn and returns the first error.
func (g *RDSLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *RDSLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := rds.NewFromConfig(config)
	g.docdbSubnetGroups = map[string]bool{}
	for _, list := range []func(*rds.Client) error{
		g.listDBClusters,
		g.listDBClusterSnapshots,
		g.listDBProxies,
		g.listDBInstances,
		g.listDBInstanceSnapshots,
		g.listDBParameterGroups,
		g.listDBSubnetGroups,
		g.listOptionGroups,
		g.listEventSubscriptions,
		g.listGlobalClusters,
	} {
		if err := list(svc); err != nil {
			return err
		}
	}
	return nil
}

// add records a resource whose ID is also its name.
func (g *RDSLister) add(id, resourceType string) {
	g.Resources = append(g.Resources, harvest.RecordBare(id, id, resourceType, "aws"))
}

// listDBClusters adds the Aurora and Multi-AZ DB clusters, and remembers
// the subnet groups of the DocumentDB clusters it leaves out.
func (g *RDSLister) listDBClusters(svc *rds.Client) error {
	p := rds.NewDescribeDBClustersPaginator(svc, &rds.DescribeDBClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, cluster := range page.DBClusters {
			if aws.ToString(cluster.Engine) == docdbEngine && aws.ToString(cluster.DBSubnetGroup) != "" {
				g.docdbSubnetGroups[aws.ToString(cluster.DBSubnetGroup)] = true
			}
			if slices.Contains(otherEngines, aws.ToString(cluster.Engine)) {
				continue
			}
			g.add(aws.ToString(cluster.DBClusterIdentifier), "aws_rds_cluster")
		}
	}
	return nil
}

// listDBClusterSnapshots adds the manual cluster snapshots, but those of
// DocumentDB and Neptune clusters, which the RDS API lists too.
func (g *RDSLister) listDBClusterSnapshots(svc *rds.Client) error {
	p := rds.NewDescribeDBClusterSnapshotsPaginator(svc, &rds.DescribeDBClusterSnapshotsInput{
		SnapshotType: aws.String(manualSnapshots),
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, snapshot := range page.DBClusterSnapshots {
			if slices.Contains(otherEngines, aws.ToString(snapshot.Engine)) {
				continue
			}
			g.add(aws.ToString(snapshot.DBClusterSnapshotIdentifier), "aws_db_cluster_snapshot")
		}
	}
	return nil
}

// listDBProxies adds the proxies.
func (g *RDSLister) listDBProxies(svc *rds.Client) error {
	p := rds.NewDescribeDBProxiesPaginator(svc, &rds.DescribeDBProxiesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, proxy := range page.DBProxies {
			g.add(aws.ToString(proxy.DBProxyName), "aws_db_proxy")
		}
	}
	return nil
}

// listDBInstances adds the instances. An instance in a cluster is an
// aws_rds_cluster_instance when the cluster is Aurora's; the instances of
// a Multi-AZ DB cluster are the cluster's own, which manages them, so
// they are left out.
func (g *RDSLister) listDBInstances(svc *rds.Client) error {
	p := rds.NewDescribeDBInstancesPaginator(svc, &rds.DescribeDBInstancesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, instance := range page.DBInstances {
			engine := aws.ToString(instance.Engine)
			if slices.Contains(otherEngines, engine) {
				continue
			}
			resourceType := "aws_db_instance"
			if aws.ToString(instance.DBClusterIdentifier) != "" {
				if !strings.HasPrefix(engine, "aurora") {
					continue
				}
				resourceType = "aws_rds_cluster_instance"
			}
			g.add(aws.ToString(instance.DBInstanceIdentifier), resourceType)
		}
	}
	return nil
}

// listDBInstanceSnapshots adds the manual instance snapshots.
func (g *RDSLister) listDBInstanceSnapshots(svc *rds.Client) error {
	p := rds.NewDescribeDBSnapshotsPaginator(svc, &rds.DescribeDBSnapshotsInput{
		SnapshotType: aws.String(manualSnapshots),
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, snapshot := range page.DBSnapshots {
			g.add(aws.ToString(snapshot.DBSnapshotIdentifier), "aws_db_snapshot")
		}
	}
	return nil
}

// listDBParameterGroups adds every parameter group but those of a
// DocumentDB or Neptune family, which the RDS API lists too. The default
// ones, which RDS provides and can't be changed, are listed too; the
// catalogue's skip rule leaves them out.
func (g *RDSLister) listDBParameterGroups(svc *rds.Client) error {
	p := rds.NewDescribeDBParameterGroupsPaginator(svc, &rds.DescribeDBParameterGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.DBParameterGroups {
			if otherEngineFamily(aws.ToString(group.DBParameterGroupFamily)) {
				continue
			}
			g.add(aws.ToString(group.DBParameterGroupName), "aws_db_parameter_group")
		}
	}
	return nil
}

// listDBSubnetGroups adds every subnet group but those DocumentDB clusters
// use (see docdbSubnetGroups), so that a group isn't imported twice. The
// default one is listed too; the catalogue's skip rule leaves it out.
func (g *RDSLister) listDBSubnetGroups(svc *rds.Client) error {
	p := rds.NewDescribeDBSubnetGroupsPaginator(svc, &rds.DescribeDBSubnetGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.DBSubnetGroups {
			if g.docdbSubnetGroups[aws.ToString(group.DBSubnetGroupName)] {
				continue
			}
			g.add(aws.ToString(group.DBSubnetGroupName), "aws_db_subnet_group")
		}
	}
	return nil
}

// listOptionGroups adds every option group. The default ones, which RDS
// provides, are listed too; the catalogue's skip rule leaves them out.
func (g *RDSLister) listOptionGroups(svc *rds.Client) error {
	p := rds.NewDescribeOptionGroupsPaginator(svc, &rds.DescribeOptionGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.OptionGroupsList {
			g.add(aws.ToString(group.OptionGroupName), "aws_db_option_group")
		}
	}
	return nil
}

// listEventSubscriptions adds the event subscriptions, by the name given
// them, which Terraform imports them with.
func (g *RDSLister) listEventSubscriptions(svc *rds.Client) error {
	p := rds.NewDescribeEventSubscriptionsPaginator(svc, &rds.DescribeEventSubscriptionsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, subscription := range page.EventSubscriptionsList {
			g.add(aws.ToString(subscription.CustSubscriptionId), "aws_db_event_subscription")
		}
	}
	return nil
}

// listGlobalClusters adds the global clusters. A global cluster spans
// regions and every region lists it, so it is added only in the region of
// its primary cluster, the writer, and an import of several regions has it
// once. One without a writer, such as one with no members yet, names no
// region, so every region that lists it adds it.
func (g *RDSLister) listGlobalClusters(svc *rds.Client) error {
	region := svc.Options().Region
	p := rds.NewDescribeGlobalClustersPaginator(svc, &rds.DescribeGlobalClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, cluster := range page.GlobalClusters {
			if primary := globalClusterRegion(cluster); primary != "" && primary != region {
				continue
			}
			g.add(aws.ToString(cluster.GlobalClusterIdentifier), "aws_rds_global_cluster")
		}
	}
	return nil
}

// globalClusterRegion returns the region of a global cluster's writer, from
// its ARN (arn:<partition>:rds:<region>:<account>:cluster:<name>), or ""
// when it has none.
func globalClusterRegion(cluster rdstypes.GlobalCluster) string {
	for _, member := range cluster.GlobalClusterMembers {
		if !aws.ToBool(member.IsWriter) {
			continue
		}
		if parts := strings.SplitN(aws.ToString(member.DBClusterArn), ":", 5); len(parts) == 5 {
			return parts[3]
		}
	}
	return ""
}
