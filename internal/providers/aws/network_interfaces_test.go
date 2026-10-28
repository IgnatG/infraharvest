// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister asks for the network interfaces AWS doesn't manage, of the
// types people create, and leaves out an instance's primary interface and
// any requester-managed one the answer still has, and those the VPC CNI
// plugin or the VPC resource controller of an EKS cluster manage, by their
// tags or their description.
func TestRecordNetworkInterfaces(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeNetworkInterfaces" {
			return recordUnexpected(t, call)
		}
		form, err := url.ParseQuery(call.Body)
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("Filter.1.Name") != "requester-managed" || form.Get("Filter.1.Value.1") != "false" || form.Get("Filter.2.Name") != "interface-type" || form.Get("Filter.2.Value.1") != "interface" {
			t.Errorf("filters: %v", form)
		}
		return recordEC2(call.Op, `<networkInterfaceSet>
			<item><networkInterfaceId>eni-own</networkInterfaceId><requesterManaged>false</requesterManaged></item>
			<item><networkInterfaceId>eni-secondary</networkInterfaceId><requesterManaged>false</requesterManaged>
				<attachment><instanceId>i-1</instanceId><deviceIndex>1</deviceIndex></attachment></item>
			<item><networkInterfaceId>eni-primary</networkInterfaceId><requesterManaged>false</requesterManaged>
				<attachment><instanceId>i-1</instanceId><deviceIndex>0</deviceIndex></attachment></item>
			<item><networkInterfaceId>eni-lambda</networkInterfaceId><requesterManaged>true</requesterManaged></item>
			<item><networkInterfaceId>eni-cni</networkInterfaceId><requesterManaged>false</requesterManaged>
				<tagSet><item><key>node.k8s.amazonaws.com/instance_id</key><value>i-2</value></item></tagSet>
				<attachment><instanceId>i-2</instanceId><deviceIndex>1</deviceIndex></attachment></item>
			<item><networkInterfaceId>eni-cluster</networkInterfaceId><requesterManaged>false</requesterManaged>
				<tagSet><item><key>cluster.k8s.amazonaws.com/name</key><value>prod</value></item></tagSet></item>
			<item><networkInterfaceId>eni-cni-untagged</networkInterfaceId><requesterManaged>false</requesterManaged><description>aws-K8S-i-2</description></item>
			<item><networkInterfaceId>eni-trunk</networkInterfaceId><requesterManaged>false</requesterManaged><description>aws-k8s-trunk-eni</description></item>
			<item><networkInterfaceId>eni-tagged</networkInterfaceId><requesterManaged>false</requesterManaged><description>app</description>
				<tagSet><item><key>Name</key><value>app</value></item></tagSet></item>
			</networkInterfaceSet>`)
	})
	g := &NetworkInterfaceLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_network_interface", "eni-own", "eni-own", nil},
		recordedResource{"aws_network_interface", "eni-secondary", "eni-secondary", nil},
		recordedResource{"aws_network_interface", "eni-tagged", "eni-tagged", nil},
	)
}
