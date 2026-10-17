// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"testing"
)

const recordDocDBNS = "http://rds.amazonaws.com/doc/2014-10-31/"

// recordDocDBAPI answers the DocumentDB lister, which shares its API with
// RDS and Neptune, so the answers hold their clusters and groups too. The
// operation failing answers with an error.
func recordDocDBAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == failing {
			return recordQueryError(recordDocDBNS, "AccessDenied")
		}
		switch call.Op {
		case "DescribeDBClusters":
			if form, _ := url.ParseQuery(call.Body); form.Get("Filters.Filter.1.Name") != "engine" || form.Get("Filters.Filter.1.Values.Value.1") != "docdb" {
				t.Errorf("clusters not asked for by the docdb engine: %s", call.Body)
			}
			// An answer that holds other engines' clusters all the same.
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordDocDBNS, `<DBClusters><DBCluster>
					<DBClusterIdentifier>orders</DBClusterIdentifier><Engine>aurora-postgresql</Engine><DBSubnetGroup>orders</DBSubnetGroup>
					<DBClusterMembers><DBClusterMember><DBInstanceIdentifier>orders-1</DBInstanceIdentifier><IsClusterWriter>true</IsClusterWriter></DBClusterMember></DBClusterMembers>
					</DBCluster><DBCluster>
					<DBClusterIdentifier>graph</DBClusterIdentifier><Engine>neptune</Engine><DBSubnetGroup>graph</DBSubnetGroup>
					</DBCluster><DBCluster>
					<DBClusterIdentifier>catalog</DBClusterIdentifier><Engine>docdb</Engine><DBSubnetGroup>default</DBSubnetGroup>
					</DBCluster><DBCluster>
					<DBClusterIdentifier>archive</DBClusterIdentifier><Engine>docdb</Engine><DBSubnetGroup>docs</DBSubnetGroup>
					</DBCluster></DBClusters>`)
			}
			return recordQuery(call.Op, recordDocDBNS, `<DBClusters><DBCluster>
				<DBClusterIdentifier>docs</DBClusterIdentifier><Engine>docdb</Engine><DBSubnetGroup>docs</DBSubnetGroup>
				<DBClusterMembers>
					<DBClusterMember><DBInstanceIdentifier>docs-1</DBInstanceIdentifier><IsClusterWriter>true</IsClusterWriter></DBClusterMember>
					<DBClusterMember><DBInstanceIdentifier>docs-2</DBInstanceIdentifier><IsClusterWriter>false</IsClusterWriter></DBClusterMember>
				</DBClusterMembers>
				</DBCluster></DBClusters><Marker>`+nextPageToken+`</Marker>`)
		case "DescribeDBClusterParameterGroups":
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordDocDBNS, `<DBClusterParameterGroups>
					<DBClusterParameterGroup><DBClusterParameterGroupName>docs-tls</DBClusterParameterGroupName><DBParameterGroupFamily>docdb5.0</DBParameterGroupFamily></DBClusterParameterGroup>
					<DBClusterParameterGroup><DBClusterParameterGroupName>graph-params</DBClusterParameterGroupName><DBParameterGroupFamily>neptune1.3</DBParameterGroupFamily></DBClusterParameterGroup>
					</DBClusterParameterGroups>`)
			}
			return recordQuery(call.Op, recordDocDBNS, `<DBClusterParameterGroups>
				<DBClusterParameterGroup><DBClusterParameterGroupName>default.docdb5.0</DBClusterParameterGroupName><DBParameterGroupFamily>docdb5.0</DBParameterGroupFamily></DBClusterParameterGroup>
				<DBClusterParameterGroup><DBClusterParameterGroupName>default.aurora-postgresql16</DBClusterParameterGroupName><DBParameterGroupFamily>aurora-postgresql16</DBParameterGroupFamily></DBClusterParameterGroup>
				<DBClusterParameterGroup><DBClusterParameterGroupName>docs-audit</DBClusterParameterGroupName><DBParameterGroupFamily>docdb4.0</DBParameterGroupFamily></DBClusterParameterGroup>
				</DBClusterParameterGroups><Marker>`+nextPageToken+`</Marker>`)
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists, across pages and in this order, the DocumentDB
// clusters, each followed by its member instances, the subnet groups they
// use but the default one, and the cluster parameter groups of a docdb
// family but the default ones, all by name. The RDS API's Aurora and
// Neptune clusters and groups are left out.
func TestRecordDocDBListsClustersSubnetAndParameterGroups(t *testing.T) {
	useFakeAPI(t, recordDocDBAPI(t, ""))
	g := &DocDBLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_docdb_cluster", "docs", "docs", nil},
		recordedResource{"aws_docdb_cluster_instance", "docs-1", "docs-1", nil},
		recordedResource{"aws_docdb_cluster_instance", "docs-2", "docs-2", nil},
		recordedResource{"aws_docdb_cluster", "catalog", "catalog", nil},
		recordedResource{"aws_docdb_cluster", "archive", "archive", nil},
		recordedResource{"aws_docdb_subnet_group", "docs", "docs", nil},
		recordedResource{"aws_docdb_cluster_parameter_group", "docs-audit", "docs-audit", nil},
		recordedResource{"aws_docdb_cluster_parameter_group", "docs-tls", "docs-tls", nil},
	)
}

// Each list fails the service.
func TestRecordDocDBReturnsErrors(t *testing.T) {
	for _, failing := range []string{"DescribeDBClusters", "DescribeDBClusterParameterGroups"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, recordDocDBAPI(t, failing))
			g := &DocDBLister{}

			assertRecordedError(t, g.List(), "AccessDenied")
		})
	}
}
