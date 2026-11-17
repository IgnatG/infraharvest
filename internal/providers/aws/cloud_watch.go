// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// CloudWatchLister lists the service "cloudwatch": the CloudWatch
// metric alarms and dashboards, and the EventBridge (CloudWatch Events)
// rules of every event bus, custom ones included, with their targets.
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
	if err := g.createRules(eventbridge.NewFromConfig(config), &parts); err != nil {
		return parts.Fail(err)
	}
	return parts.Err()
}

// listMetricAlarms records every metric alarm by name. Composite alarms
// are not listed.
func (g *CloudWatchLister) listMetricAlarms(client *cloudwatch.Client) error {
	for page, err := range pagesOf(g.Context(), cloudwatch.NewDescribeAlarmsPaginator(client, &cloudwatch.DescribeAlarmsInput{}, stopOnDuplicateToken)) {
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
	for page, err := range pagesOf(g.Context(), cloudwatch.NewListDashboardsPaginator(client, &cloudwatch.ListDashboardsInput{}, stopOnDuplicateToken)) {
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

// defaultEventBus is the name of the event bus every account has, whose
// rules and targets Terraform imports without the bus in their IDs.
const defaultEventBus = "default"

// createRules records the rules of every event bus by name, each followed
// by its targets: the default bus first, then the custom buses the account
// lists. A rule on a custom bus has the ID "<bus>/<rule>" and its bus as
// event_bus_name, as Terraform imports it. A rule that another AWS service
// manages (it has ManagedBy set) is recorded with managed_by, which the
// catalogue skips, with its reason, as that service owns it and it can't be
// changed through Terraform; its targets aren't asked for. The API has no
// SDK paginator, so pages are walked by NextToken. A rule whose targets
// can't be read keeps those read, and the failure goes to parts, as does
// that of a custom bus's rules, or of the bus list, which leaves the
// default bus alone; the default bus's rules failing fails the service.
func (g *CloudWatchLister) createRules(client *eventbridge.Client, parts *harvest.Parts) error {
	if err := g.createBusRules(client, defaultEventBus, parts); err != nil {
		return err
	}
	buses, err := g.customEventBuses(client)
	parts.Add(awsCodes, "the event buses", err)
	for _, bus := range buses {
		err := g.createBusRules(client, bus, parts)
		parts.Add(awsCodes, "the rules of event bus "+bus, err)
	}
	return nil
}

// customEventBuses returns the names of the account's event buses but the
// default one, those listed before an error included.
func (g *CloudWatchLister) customEventBuses(client *eventbridge.Client) ([]string, error) {
	var buses []string
	err := paginateByMarker(func(token *string) (*string, error) {
		page, err := client.ListEventBuses(g.Context(), &eventbridge.ListEventBusesInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, bus := range page.EventBuses {
			if name := aws.ToString(bus.Name); name != "" && name != defaultEventBus {
				buses = append(buses, name)
			}
		}
		return page.NextToken, nil
	})
	return buses, err
}

// createBusRules records the rules of the event bus bus (see createRules),
// each followed by its targets, whose failures go to parts.
func (g *CloudWatchLister) createBusRules(client *eventbridge.Client, bus string, parts *harvest.Parts) error {
	custom := bus != defaultEventBus
	return paginateByMarker(func(token *string) (*string, error) {
		input := &eventbridge.ListRulesInput{NextToken: token}
		if custom {
			input.EventBusName = aws.String(bus)
		}
		page, err := client.ListRules(g.Context(), input)
		if err != nil {
			return nil, err
		}
		for _, rule := range page.Rules {
			name := aws.ToString(rule.Name)
			id, attributes := name, map[string]string{}
			if custom {
				id = bus + "/" + name
			}
			if rule.ManagedBy != nil {
				attributes["managed_by"] = aws.ToString(rule.ManagedBy)
				g.Resources = append(g.Resources, harvest.Record(id, id, "aws_cloudwatch_event_rule", "aws", attributes))
				continue
			}
			mark := len(g.Resources)
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_cloudwatch_event_rule", "aws", attributes))
			// A call per rule, for targets the import may not keep.
			if !g.Wants("aws_cloudwatch_event_target") {
				continue
			}
			err := g.createTargets(client, bus, name)
			if !dropIfGone(&g.Resources, mark, err) {
				parts.Add(awsCodes, "the targets of rule "+id, err)
			}
		}
		return page.NextToken, nil
	})
}

// createTargets records the targets of the rule ruleName on the event bus
// bus as "<rule>/<target ID>", Terraform's import ID for the default bus,
// and "<bus>/<rule>/<target ID>" for a custom one. Its pages are walked
// with the target list's own NextToken.
func (g *CloudWatchLister) createTargets(client *eventbridge.Client, bus, ruleName string) error {
	custom := bus != defaultEventBus
	return paginateByMarker(func(token *string) (*string, error) {
		input := &eventbridge.ListTargetsByRuleInput{
			Rule:      aws.String(ruleName),
			NextToken: token,
		}
		if custom {
			input.EventBusName = aws.String(bus)
		}
		page, err := client.ListTargetsByRule(g.Context(), input)
		if err != nil {
			return nil, err
		}
		for _, target := range page.Targets {
			targetID := aws.ToString(target.Id)
			id := ruleName + "/" + targetID
			attributes := map[string]string{"rule": ruleName, "target_id": targetID}
			if custom {
				id = bus + "/" + id
				attributes["event_bus_name"] = bus
			}
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_cloudwatch_event_target", "aws", attributes))
		}
		return page.NextToken, nil
	})
}
