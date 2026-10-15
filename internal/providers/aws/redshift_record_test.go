// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

const recordRedshiftNS = "http://redshift.amazonaws.com/doc/2012-12-01/"

// recordRedshiftPages are the two pages of each Redshift list, as the
// result's inner XML; the first page ends with a marker.
var recordRedshiftPages = map[string][2]string{
	"DescribeClusters": {
		`<Clusters><Cluster><ClusterIdentifier>analytics</ClusterIdentifier><ClusterStatus>available</ClusterStatus></Cluster></Clusters>`,
		`<Clusters><Cluster><ClusterIdentifier>reports</ClusterIdentifier><ClusterStatus>available</ClusterStatus></Cluster></Clusters>`,
	},
	"DescribeClusterParameterGroups": {
		`<ParameterGroups>
			<ClusterParameterGroup><ParameterGroupName>default.redshift-1.0</ParameterGroupName><ParameterGroupFamily>redshift-1.0</ParameterGroupFamily></ClusterParameterGroup>
			<ClusterParameterGroup><ParameterGroupName>etl-params</ParameterGroupName><ParameterGroupFamily>redshift-1.0</ParameterGroupFamily></ClusterParameterGroup>
			</ParameterGroups>`,
		`<ParameterGroups><ClusterParameterGroup><ParameterGroupName>load-params</ParameterGroupName></ClusterParameterGroup></ParameterGroups>`,
	},
	"DescribeClusterSubnetGroups": {
		`<ClusterSubnetGroups><ClusterSubnetGroup><ClusterSubnetGroupName>default</ClusterSubnetGroupName><VpcId>vpc-1</VpcId></ClusterSubnetGroup></ClusterSubnetGroups>`,
		`<ClusterSubnetGroups><ClusterSubnetGroup><ClusterSubnetGroupName>warehouse</ClusterSubnetGroupName><VpcId>vpc-1</VpcId></ClusterSubnetGroup></ClusterSubnetGroups>`,
	},
	"DescribeEventSubscriptions": {
		`<EventSubscriptionsList><EventSubscription><CustomerAwsId>123456789012</CustomerAwsId><CustSubscriptionId>alerts</CustSubscriptionId><Status>active</Status></EventSubscription></EventSubscriptionsList>`,
		`<EventSubscriptionsList><EventSubscription><CustomerAwsId>123456789012</CustomerAwsId><CustSubscriptionId>audits</CustSubscriptionId><Status>active</Status></EventSubscription></EventSubscriptionsList>`,
	},
	"DescribeSnapshotSchedules": {
		`<SnapshotSchedules><SnapshotSchedule><ScheduleIdentifier>nightly</ScheduleIdentifier>
			<AssociatedClusters>
				<ClusterAssociatedToSchedule><ClusterIdentifier>analytics</ClusterIdentifier><ScheduleAssociationState>ACTIVE</ScheduleAssociationState></ClusterAssociatedToSchedule>
				<ClusterAssociatedToSchedule><ClusterIdentifier>reports</ClusterIdentifier><ScheduleAssociationState>ACTIVE</ScheduleAssociationState></ClusterAssociatedToSchedule>
			</AssociatedClusters></SnapshotSchedule></SnapshotSchedules>`,
		`<SnapshotSchedules><SnapshotSchedule><ScheduleIdentifier>weekly</ScheduleIdentifier></SnapshotSchedule></SnapshotSchedules>`,
	},
}

func recordRedshiftAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == failing {
			return recordQueryError(recordRedshiftNS, "AccessDenied")
		}
		pages, ok := recordRedshiftPages[call.Op]
		if !ok {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return recordQuery(call.Op, recordRedshiftNS, pages[1])
		}
		return recordQuery(call.Op, recordRedshiftNS, pages[0]+`<Marker>`+nextPageToken+`</Marker>`)
	}
}

// The lister lists, each across pages: clusters; parameter groups but the
// defaults (names with a dot); every subnet group;
// event subscriptions by name; snapshot schedules, each followed by an
// association per cluster it is associated with.
func TestRecordRedshiftListsClustersAndTheirParts(t *testing.T) {
	useFakeAPI(t, recordRedshiftAPI(t, ""))
	g := &RedshiftLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_redshift_cluster", "analytics", "analytics", nil},
		recordedResource{"aws_redshift_cluster", "reports", "reports", nil},
		recordedResource{"aws_redshift_parameter_group", "etl-params", "etl-params", nil},
		recordedResource{"aws_redshift_parameter_group", "load-params", "load-params", nil},
		recordedResource{"aws_redshift_subnet_group", "default", "default", nil},
		recordedResource{"aws_redshift_subnet_group", "warehouse", "warehouse", nil},
		recordedResource{"aws_redshift_event_subscription", "alerts", "alerts", nil},
		recordedResource{"aws_redshift_event_subscription", "audits", "audits", nil},
		recordedResource{"aws_redshift_snapshot_schedule", "nightly", "nightly", nil},
		recordedResource{"aws_redshift_snapshot_schedule_association", "analytics/nightly", "analytics_nightly", nil},
		recordedResource{"aws_redshift_snapshot_schedule_association", "reports/nightly", "reports_nightly", nil},
		recordedResource{"aws_redshift_snapshot_schedule", "weekly", "weekly", nil},
	)
}

func TestRecordRedshiftReturnsErrors(t *testing.T) {
	for failing := range recordRedshiftPages {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, recordRedshiftAPI(t, failing))
			g := &RedshiftLister{}

			err := g.List()

			assertRecordedError(t, err, "AccessDenied")
			if err != nil && !strings.Contains(err.Error(), failing) {
				t.Errorf("want the %s error, got %v", failing, err)
			}
		})
	}
}
