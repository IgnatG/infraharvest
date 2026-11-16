// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchevents"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// CloudWatchLister lists the service "cloudwatch": the CloudWatch
// metric alarms and dashboards, and the EventBridge (CloudWatch Events)
// rules of the default event bus with their targets.
type CloudWatchLister struct {
	Service
}

// List lists, in this order, metric alarms, dashboards, and event
// rules with their targets. A rule whose targets can't be read is still
// recorded, with the targets read before the failure, and the failure
// makes the listing partial (see harvest.Parts); a rule deleted since it
// was listed has none. Any other error fails the service.
func (g *CloudWatchLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *CloudWatchLister) list() error {
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
	var parts harvest.Parts
	if err := g.createRules(cloudwatchevents.NewFromConfig(config), &parts); err != nil {
		return parts.Fail(err)
	}
	return parts.Err()
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
// pages are walked by NextToken. A rule whose targets can't be read keeps
// those read, and the failure goes to parts.
func (g *CloudWatchLister) createRules(client *cloudwatchevents.Client, parts *harvest.Parts) error {
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
			if !g.Wants("aws_cloudwatch_event_target") {
				continue
			}
			parts.Add(awsCodes, "the targets of rule "+name, g.createTargets(client, name))
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
