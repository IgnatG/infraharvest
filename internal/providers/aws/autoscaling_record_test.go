// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

const recordAutoScalingNS = "http://autoscaling.amazonaws.com/doc/2011-01-01/"

// The lister lists, across pages and in this order, Auto Scaling groups by
// name with fixed attributes, launch configurations by name, with
// user_data_base64 set to "=" when they have user data, and EC2 launch
// templates by ID, named after their name.
func TestRecordAutoScalingListsGroupsConfigurationsAndTemplates(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeAutoScalingGroups":
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordAutoScalingNS, `<AutoScalingGroups><member>
					<AutoScalingGroupName>batch</AutoScalingGroupName><MinSize>0</MinSize><MaxSize>4</MaxSize>
					</member></AutoScalingGroups>`)
			}
			return recordQuery(call.Op, recordAutoScalingNS, `<AutoScalingGroups><member>
				<AutoScalingGroupName>web</AutoScalingGroupName><MinSize>2</MinSize><MaxSize>6</MaxSize>
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
				</item></launchTemplates><nextToken>`+nextPageToken+`</nextToken>`)
		}
		return recordUnexpected(t, call)
	})
	g := &AutoScalingLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	groupAttributes := map[string]string{
		"force_delete":              "false",
		"metrics_granularity":       "1Minute",
		"wait_for_capacity_timeout": "10m",
	}
	assertRecorded(t, g.Resources,
		recordedResource{"aws_autoscaling_group", "web", "web", groupAttributes},
		recordedResource{"aws_autoscaling_group", "batch", "batch", groupAttributes},
		recordedResource{"aws_launch_configuration", "web-v3", "web-v3", map[string]string{"user_data_base64": "="}},
		recordedResource{"aws_launch_configuration", "batch-v1", "batch-v1", nil},
		recordedResource{"aws_launch_template", "lt-0a1", "web", nil},
		recordedResource{"aws_launch_template", "lt-0b2", "batch", nil},
	)
}

// Each of the three lists fails the service.
func TestRecordAutoScalingReturnsErrors(t *testing.T) {
	for _, failing := range []string{"DescribeAutoScalingGroups", "DescribeLaunchConfigurations", "DescribeLaunchTemplates"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
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

			assertRecordedError(t, g.List(), "UnauthorizedOperation")
		})
	}
}
