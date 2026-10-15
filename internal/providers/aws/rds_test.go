// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

// rdsResponse wraps the result of a call of the RDS query API.
func rdsResponse(op, result string) string {
	return `<` + op + `Response xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><` + op + `Result>` + result +
		`</` + op + `Result></` + op + `Response>`
}

// The lister lists what the RDS types manage: not DocumentDB's or
// Neptune's clusters and instances, which the RDS API lists too, nor the
// instances of a Multi-AZ DB cluster, which it manages; an Aurora
// cluster's instances as aws_rds_cluster_instance; manual snapshots only;
// every parameter, subnet and option group, the default ones included,
// which the catalogue leaves out; event subscriptions by name.
func TestRDSListsWhatTerraformManages(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeDBClusters":
			return rdsResponse(call.Op, `<DBClusters>
				<DBCluster><DBClusterIdentifier>orders</DBClusterIdentifier><Engine>aurora-postgresql</Engine></DBCluster>
				<DBCluster><DBClusterIdentifier>reports</DBClusterIdentifier><Engine>postgres</Engine></DBCluster>
				<DBCluster><DBClusterIdentifier>docs</DBClusterIdentifier><Engine>docdb</Engine></DBCluster>
				<DBCluster><DBClusterIdentifier>graph</DBClusterIdentifier><Engine>neptune</Engine></DBCluster>
				</DBClusters>`)
		case "DescribeDBInstances":
			return rdsResponse(call.Op, `<DBInstances>
				<DBInstance><DBInstanceIdentifier>app</DBInstanceIdentifier><Engine>postgres</Engine></DBInstance>
				<DBInstance><DBInstanceIdentifier>orders-1</DBInstanceIdentifier><Engine>aurora-postgresql</Engine><DBClusterIdentifier>orders</DBClusterIdentifier></DBInstance>
				<DBInstance><DBInstanceIdentifier>reports-1</DBInstanceIdentifier><Engine>postgres</Engine><DBClusterIdentifier>reports</DBClusterIdentifier></DBInstance>
				<DBInstance><DBInstanceIdentifier>docs-1</DBInstanceIdentifier><Engine>docdb</Engine><DBClusterIdentifier>docs</DBClusterIdentifier></DBInstance>
				<DBInstance><DBInstanceIdentifier>graph-1</DBInstanceIdentifier><Engine>neptune</Engine><DBClusterIdentifier>graph</DBClusterIdentifier></DBInstance>
				</DBInstances>`)
		case "DescribeDBSnapshots", "DescribeDBClusterSnapshots":
			if !strings.Contains(call.Body, "SnapshotType=manual") {
				t.Errorf("%s of every type: %s", call.Op, call.Body)
			}
			if call.Op == "DescribeDBSnapshots" {
				return rdsResponse(call.Op, `<DBSnapshots><DBSnapshot><DBSnapshotIdentifier>app-before-upgrade</DBSnapshotIdentifier></DBSnapshot></DBSnapshots>`)
			}
			return rdsResponse(call.Op, `<DBClusterSnapshots><DBClusterSnapshot><DBClusterSnapshotIdentifier>orders-final</DBClusterSnapshotIdentifier></DBClusterSnapshot></DBClusterSnapshots>`)
		case "DescribeDBProxies":
			return rdsResponse(call.Op, `<DBProxies><member><DBProxyName>pool</DBProxyName></member></DBProxies>`)
		case "DescribeDBParameterGroups":
			return rdsResponse(call.Op, `<DBParameterGroups>
				<DBParameterGroup><DBParameterGroupName>default.postgres17</DBParameterGroupName></DBParameterGroup>
				<DBParameterGroup><DBParameterGroupName>app-postgres17</DBParameterGroupName></DBParameterGroup>
				</DBParameterGroups>`)
		case "DescribeDBSubnetGroups":
			// The catalogue leaves out the default one.
			return rdsResponse(call.Op, `<DBSubnetGroups>
				<DBSubnetGroup><DBSubnetGroupName>default</DBSubnetGroupName></DBSubnetGroup>
				<DBSubnetGroup><DBSubnetGroupName>app</DBSubnetGroupName></DBSubnetGroup>
				</DBSubnetGroups>`)
		case "DescribeOptionGroups":
			return rdsResponse(call.Op, `<OptionGroupsList>
				<OptionGroup><OptionGroupName>default:postgres-17</OptionGroupName></OptionGroup>
				<OptionGroup><OptionGroupName>app-options</OptionGroupName></OptionGroup>
				</OptionGroupsList>`)
		case "DescribeEventSubscriptions":
			return rdsResponse(call.Op, `<EventSubscriptionsList><EventSubscription>
				<CustSubscriptionId>alerts</CustSubscriptionId><CustomerAwsId>123456789012</CustomerAwsId>
				</EventSubscription></EventSubscriptionsList>`)
		case "DescribeGlobalClusters":
			return rdsResponse(call.Op, `<GlobalClusters/>`)
		}
		t.Errorf("unexpected call %s", call.Op)
		return `<ErrorResponse><Error><Code>Unexpected</Code></Error></ErrorResponse>`
	})
	g := &RDSLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_rds_cluster", "orders", "reports")
	assertIDs(t, g.Resources, "aws_rds_cluster_instance", "orders-1")
	assertIDs(t, g.Resources, "aws_db_instance", "app")
	assertIDs(t, g.Resources, "aws_db_snapshot", "app-before-upgrade")
	assertIDs(t, g.Resources, "aws_db_cluster_snapshot", "orders-final")
	assertIDs(t, g.Resources, "aws_db_proxy", "pool")
	assertIDs(t, g.Resources, "aws_db_parameter_group", "default.postgres17", "app-postgres17")
	assertIDs(t, g.Resources, "aws_db_subnet_group", "default", "app")
	assertIDs(t, g.Resources, "aws_db_option_group", "default:postgres-17", "app-options")
	assertIDs(t, g.Resources, "aws_db_event_subscription", "alerts")
}

// Every region lists a global cluster, so a region lists it only when its
// writer, the primary cluster, is there; one without a writer names no
// region, and every region lists it.
func TestRDSListsGlobalClustersInTheirPrimaryRegion(t *testing.T) {
	member := func(region, cluster string, writer bool) string {
		isWriter := "false"
		if writer {
			isWriter = "true"
		}
		return `<GlobalClusterMember><DBClusterArn>arn:aws:rds:` + region + `:123456789012:cluster:` + cluster +
			`</DBClusterArn><IsWriter>` + isWriter + `</IsWriter></GlobalClusterMember>`
	}
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeGlobalClusters":
			// The fake API's region is us-east-1.
			return rdsResponse(call.Op, `<GlobalClusters>
				<GlobalClusterMember><GlobalClusterIdentifier>orders</GlobalClusterIdentifier><GlobalClusterMembers>`+
				member("us-east-1", "orders-use1", true)+member("eu-west-1", "orders-euw1", false)+`</GlobalClusterMembers></GlobalClusterMember>
				<GlobalClusterMember><GlobalClusterIdentifier>reports</GlobalClusterIdentifier><GlobalClusterMembers>`+
				member("us-east-1", "reports-use1", false)+member("eu-west-1", "reports-euw1", true)+`</GlobalClusterMembers></GlobalClusterMember>
				<GlobalClusterMember><GlobalClusterIdentifier>empty</GlobalClusterIdentifier><GlobalClusterMembers/></GlobalClusterMember>
				</GlobalClusters>`)
		case "DescribeDBClusters":
			return rdsResponse(call.Op, `<DBClusters/>`)
		case "DescribeDBClusterSnapshots":
			return rdsResponse(call.Op, `<DBClusterSnapshots/>`)
		case "DescribeDBProxies":
			return rdsResponse(call.Op, `<DBProxies/>`)
		case "DescribeDBInstances":
			return rdsResponse(call.Op, `<DBInstances/>`)
		case "DescribeDBSnapshots":
			return rdsResponse(call.Op, `<DBSnapshots/>`)
		case "DescribeDBParameterGroups":
			return rdsResponse(call.Op, `<DBParameterGroups/>`)
		case "DescribeDBSubnetGroups":
			return rdsResponse(call.Op, `<DBSubnetGroups/>`)
		case "DescribeOptionGroups":
			return rdsResponse(call.Op, `<OptionGroupsList/>`)
		case "DescribeEventSubscriptions":
			return rdsResponse(call.Op, `<EventSubscriptionsList/>`)
		}
		t.Errorf("unexpected call %s", call.Op)
		return `<ErrorResponse><Error><Code>Unexpected</Code></Error></ErrorResponse>`
	})
	g := &RDSLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_rds_global_cluster", "orders", "empty")
}
