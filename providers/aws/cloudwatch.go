// Copyright 2020 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchevents"
)

var cloudwatchAllowEmptyValues = []string{"tags."}

type CloudWatchGenerator struct {
	AWSService
}

func (g *CloudWatchGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}

	cloudwatchSvc := cloudwatch.NewFromConfig(config)
	err := g.createMetricAlarms(cloudwatchSvc)
	if err != nil {
		return err
	}
	err = g.createDashboards(cloudwatchSvc)
	if err != nil {
		return err
	}

	cloudwatcheventsSvc := cloudwatchevents.NewFromConfig(config)
	err = g.createRules(cloudwatcheventsSvc)
	if err != nil {
		return err
	}

	return nil
}

func (g *CloudWatchGenerator) createMetricAlarms(cloudwatchSvc *cloudwatch.Client) error {
	return paginateByMarker(func(nextToken *string) (*string, error) {
		output, err := cloudwatchSvc.DescribeAlarms(g.Context(), &cloudwatch.DescribeAlarmsInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, metricAlarm := range output.MetricAlarms {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*metricAlarm.AlarmName,
				*metricAlarm.AlarmName,
				"aws_cloudwatch_metric_alarm",
				"aws",
				cloudwatchAllowEmptyValues))
		}
		return output.NextToken, nil
	})
}

func (g *CloudWatchGenerator) createDashboards(cloudwatchSvc *cloudwatch.Client) error {
	return paginateByMarker(func(nextToken *string) (*string, error) {
		output, err := cloudwatchSvc.ListDashboards(g.Context(), &cloudwatch.ListDashboardsInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, dashboardEntry := range output.DashboardEntries {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*dashboardEntry.DashboardName,
				*dashboardEntry.DashboardName,
				"aws_cloudwatch_dashboard",
				"aws",
				cloudwatchAllowEmptyValues))
		}
		return output.NextToken, nil
	})
}

func (g *CloudWatchGenerator) createRules(cloudwatcheventsSvc *cloudwatchevents.Client) error {
	return paginateByMarker(func(nextToken *string) (*string, error) {
		output, err := cloudwatcheventsSvc.ListRules(g.Context(), &cloudwatchevents.ListRulesInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, rule := range output.Rules {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*rule.Name,
				*rule.Name,
				"aws_cloudwatch_event_rule",
				"aws",
				cloudwatchAllowEmptyValues))
			if err := g.createTargets(cloudwatcheventsSvc, rule.Name); err != nil {
				return nil, err
			}
		}
		return output.NextToken, nil
	})
}

func (g *CloudWatchGenerator) createTargets(cloudwatcheventsSvc *cloudwatchevents.Client, ruleName *string) error {
	return paginateByMarker(func(nextToken *string) (*string, error) {
		output, err := cloudwatcheventsSvc.ListTargetsByRule(g.Context(), &cloudwatchevents.ListTargetsByRuleInput{
			Rule:      ruleName,
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, target := range output.Targets {
			targetRef := *ruleName + "/" + *target.Id
			g.Resources = append(g.Resources, terraformutils.NewResource(
				targetRef,
				targetRef,
				"aws_cloudwatch_event_target",
				"aws",
				map[string]string{
					"rule":      *ruleName,
					"target_id": *target.Id,
				},
				cloudwatchAllowEmptyValues,
				map[string]interface{}{}))
		}
		return output.NextToken, nil
	})
}
