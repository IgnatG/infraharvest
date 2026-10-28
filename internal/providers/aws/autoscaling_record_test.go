// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"maps"
	"testing"
)

const recordAutoScalingNS = "http://autoscaling.amazonaws.com/doc/2011-01-01/"

// The lister lists, across pages and in this order, Auto Scaling groups by
// name with fixed attributes and their tags, launch configurations by name,
// with user_data_base64 set to "=" when they have user data, and EC2 launch
// templates by ID, named after their name, with their tags.
func TestRecordAutoScalingListsGroupsConfigurationsAndTemplates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeAutoScalingGroups":
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordAutoScalingNS, `<AutoScalingGroups><member>
					<AutoScalingGroupName>batch</AutoScalingGroupName><MinSize>0</MinSize><MaxSize>4</MaxSize>
					</member></AutoScalingGroups>`)
			}
			return recordQuery(call.Op, recordAutoScalingNS, `<AutoScalingGroups><member>
				<AutoScalingGroupName>web</AutoScalingGroupName><MinSize>2</MinSize><MaxSize>6</MaxSize>
				<Tags><member><ResourceId>web</ResourceId><ResourceType>auto-scaling-group</ResourceType><Key>eks:nodegroup-name</Key><Value>workers</Value><PropagateAtLaunch>true</PropagateAtLaunch></member></Tags>
				</member></AutoScalingGroups><NextToken>`+nextPageToken+`</NextToken>`)
		case "DescribeLaunchConfigurations":
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordAutoScalingNS, `<LaunchConfigurations><member>
					<LaunchConfigurationName>batch-v1</LaunchConfigurationName><ImageId>ami-2</ImageId><UserData></UserData>
					</member></LaunchConfigurations>`)
			}
			return recordQuery(call.Op, recordAutoScalingNS, `<LaunchConfigurations><member>
				<LaunchConfigurationName>web-v3</LaunchConfigurationName><ImageId>ami-1</ImageId><UserData>IyEvYmluL2Jhc2gK</UserData>
				</member></LaunchConfigurations><NextToken>`+nextPageToken+`</NextToken>`)
		case "DescribeLaunchTemplates":
			if recordSecondPage(call) {
				return recordEC2(call.Op, `<launchTemplates><item>
					<launchTemplateId>lt-0b2</launchTemplateId><launchTemplateName>batch</launchTemplateName>
					</item></launchTemplates>`)
			}
			return recordEC2(call.Op, `<launchTemplates><item>
				<launchTemplateId>lt-0a1</launchTemplateId><launchTemplateName>web</launchTemplateName>
				<tagSet><item><key>eks:nodegroup-name</key><value>workers</value></item></tagSet>
				</item></launchTemplates><nextToken>`+nextPageToken+`</nextToken>`)
		}
		return recordUnexpected(t, call)
	})
	g := &AutoScalingLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	groupAttributes := map[string]string{
		"force_delete":              "false",
		"metrics_granularity":       "1Minute",
		"wait_for_capacity_timeout": "10m",
	}
	webAttributes := maps.Clone(groupAttributes)
	webAttributes["tags.eks:nodegroup-name"] = "workers"
	assertRecorded(t, g.Resources,
		recordedResource{"aws_autoscaling_group", "web", "web", webAttributes},
		recordedResource{"aws_autoscaling_group", "batch", "batch", groupAttributes},
		recordedResource{"aws_launch_configuration", "web-v3", "web-v3", map[string]string{"user_data_base64": "="}},
		recordedResource{"aws_launch_configuration", "batch-v1", "batch-v1", nil},
		recordedResource{"aws_launch_template", "lt-0a1", "web", map[string]string{"tags.eks:nodegroup-name": "workers"}},
		recordedResource{"aws_launch_template", "lt-0b2", "batch", nil},
	)
}

// Each of the three lists fails the service.
func TestRecordAutoScalingReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"DescribeAutoScalingGroups", "DescribeLaunchConfigurations", "DescribeLaunchTemplates"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					if failing == "DescribeLaunchTemplates" {
						return recordEC2Error()
					}
					return recordQueryError(recordAutoScalingNS, "UnauthorizedOperation")
				case "DescribeAutoScalingGroups":
					return recordQuery(call.Op, recordAutoScalingNS, `<AutoScalingGroups/>`)
				case "DescribeLaunchConfigurations":
					return recordQuery(call.Op, recordAutoScalingNS, `<LaunchConfigurations/>`)
				}
				return recordUnexpected(t, call)
			})
			g := &AutoScalingLister{}
			g.SetContext(ctx)

			assertRecordedError(t, g.List(), "UnauthorizedOperation")
		})
	}
}
