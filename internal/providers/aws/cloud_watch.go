// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchevents"
)

// CloudWatchLister lists the service "cloudwatch": the CloudWatch
// metric alarms and dashboards, and the EventBridge (CloudWatch Events)
// rules of the default event bus with their targets.
type CloudWatchLister struct {
	Service
}

// List lists, in this order, metric alarms, dashboards, and event
// rules with their targets. Any error fails the service.
func (g *CloudWatchLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := cloudwatch.NewFromConfig(config)
	if err := g.listMetricAlarms(client); err != nil {
		return err
	}
	if err := g.listDashboards(client); err != nil {
		return err
	}
	return g.createRules(cloudwatchevents.NewFromConfig(config))
}

// listMetricAlarms records every metric alarm by name. Composite alarms
// are not listed.
func (g *CloudWatchLister) listMetricAlarms(client *cloudwatch.Client) error {
	p := cloudwatch.NewDescribeAlarmsPaginator(client, &cloudwatch.DescribeAlarmsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, alarm := range page.MetricAlarms {
			name := aws.ToString(alarm.AlarmName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_cloudwatch_metric_alarm", "aws"))
		}
	}
	return nil
}

// listDashboards records every dashboard by name.
func (g *CloudWatchLister) listDashboards(client *cloudwatch.Client) error {
	p := cloudwatch.NewListDashboardsPaginator(client, &cloudwatch.ListDashboardsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, dashboard := range page.DashboardEntries {
			name := aws.ToString(dashboard.DashboardName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_cloudwatch_dashboard", "aws"))
		}
	}
	return nil
}

// createRules records the rules of the default event bus by name, each
// followed by its targets. A rule that another AWS service manages (it has
// ManagedBy set) is left out with its targets: that service owns it, and
// it cannot be changed through Terraform. The API has no SDK paginator, so
// pages are walked by NextToken.
func (g *CloudWatchLister) createRules(client *cloudwatchevents.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		page, err := client.ListRules(g.Context(), &cloudwatchevents.ListRulesInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, rule := range page.Rules {
			if rule.ManagedBy != nil {
				continue
			}
			name := aws.ToString(rule.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_cloudwatch_event_rule", "aws"))
			// A call per rule, for targets the import may not keep.
			if !g.wants("aws_cloudwatch_event_target") {
				continue
			}
			if err := g.createTargets(client, name); err != nil {
				return nil, err
			}
		}
		return page.NextToken, nil
	})
}

// createTargets records the targets of the rule ruleName as
// "<rule>/<target ID>", Terraform's import ID for the default bus. Its
// pages are walked with the target list's own NextToken.
func (g *CloudWatchLister) createTargets(client *cloudwatchevents.Client, ruleName string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		page, err := client.ListTargetsByRule(g.Context(), &cloudwatchevents.ListTargetsByRuleInput{
			Rule:      aws.String(ruleName),
			NextToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, target := range page.Targets {
			targetID := aws.ToString(target.Id)
			id := ruleName + "/" + targetID
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_cloudwatch_event_target", "aws", map[string]string{
				"rule":      ruleName,
				"target_id": targetID,
			}))
		}
		return page.NextToken, nil
	})
}
