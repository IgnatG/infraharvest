// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// cloudControlType is a resource type listed through the Cloud Control
// API, which lists any CloudFormation type with one call: its
// CloudFormation type, and the Terraform type it imports as.
type cloudControlType struct {
	CloudFormation string
	// Identifier is the one property of the CloudFormation type's primary
	// identifier, whose value Cloud Control returns as each resource's
	// identifier, and which is also the Terraform type's import ID: a type
	// whose identifier isn't, or has several properties, needs the ID
	// built. The CloudFormation schemas are checked against it (see
	// TestCloudControlIdentifiersMatchSchemas); the Terraform documentation
	// was, when the type was added.
	Identifier string
	Terraform  string
	// CreatedByAWS, if set, reports whether AWS created the resource with
	// this ID for the account, such as the default event bus: the default
	// selection leaves those out.
	CreatedByAWS func(id string) bool
}

// cloudControlServices are the services listed through Cloud Control, by
// name, each a group of related types the service listers don't cover.
var cloudControlServices = map[string][]cloudControlType{
	"appconfig": {
		{CloudFormation: "AWS::AppConfig::Application", Identifier: "ApplicationId", Terraform: "aws_appconfig_application"},
	},
	"athena": {
		{CloudFormation: "AWS::Athena::WorkGroup", Identifier: "Name", Terraform: "aws_athena_workgroup", CreatedByAWS: named("primary")},
	},
	"backup": {
		{CloudFormation: "AWS::Backup::BackupPlan", Identifier: "BackupPlanId", Terraform: "aws_backup_plan"},
		{CloudFormation: "AWS::Backup::BackupVault", Identifier: "BackupVaultName", Terraform: "aws_backup_vault", CreatedByAWS: named("Default", "aws/efs/automatic-backup-vault")},
	},
	"codeartifact": {
		{CloudFormation: "AWS::CodeArtifact::Domain", Identifier: "Arn", Terraform: "aws_codeartifact_domain"},
		{CloudFormation: "AWS::CodeArtifact::Repository", Identifier: "Arn", Terraform: "aws_codeartifact_repository"},
	},
	"eventbridge": {
		{CloudFormation: "AWS::Events::ApiDestination", Identifier: "Name", Terraform: "aws_cloudwatch_event_api_destination"},
		{CloudFormation: "AWS::Events::Archive", Identifier: "ArchiveName", Terraform: "aws_cloudwatch_event_archive"},
		{CloudFormation: "AWS::Events::Connection", Identifier: "Name", Terraform: "aws_cloudwatch_event_connection"},
		{CloudFormation: "AWS::Events::EventBus", Identifier: "Name", Terraform: "aws_cloudwatch_event_bus", CreatedByAWS: named("default")},
	},
	"guardduty": {
		{CloudFormation: "AWS::GuardDuty::Detector", Identifier: "Id", Terraform: "aws_guardduty_detector"},
	},
	"pipes": {
		{CloudFormation: "AWS::Pipes::Pipe", Identifier: "Name", Terraform: "aws_pipes_pipe"},
	},
	"placement_group": {
		{CloudFormation: "AWS::EC2::PlacementGroup", Identifier: "GroupName", Terraform: "aws_placement_group"},
	},
	"route53resolver": {
		{CloudFormation: "AWS::Route53Resolver::ResolverEndpoint", Identifier: "ResolverEndpointId", Terraform: "aws_route53_resolver_endpoint"},
		{CloudFormation: "AWS::Route53Resolver::ResolverRule", Identifier: "ResolverRuleId", Terraform: "aws_route53_resolver_rule", CreatedByAWS: func(id string) bool {
			// Such as the Internet Resolver rule.
			return strings.HasPrefix(id, "rslvr-autodefined-")
		}},
	},
	"scheduler": {
		{CloudFormation: "AWS::Scheduler::ScheduleGroup", Identifier: "Name", Terraform: "aws_scheduler_schedule_group", CreatedByAWS: named("default")},
	},
	"synthetics": {
		{CloudFormation: "AWS::Synthetics::Canary", Identifier: "Name", Terraform: "aws_synthetics_canary"},
	},
}

// named returns a CreatedByAWS that matches ids.
func named(ids ...string) func(string) bool {
	return func(id string) bool { return slices.Contains(ids, id) }
}

// cloudControlTypeOf finds a Terraform type's Cloud Control type.
func cloudControlTypeOf(terraformType string) (cloudControlType, bool) {
	for _, types := range cloudControlServices {
		for _, t := range types {
			if t.Terraform == terraformType {
				return t, true
			}
		}
	}
	return cloudControlType{}, false
}

// CloudControlGenerator lists a service of cloudControlServices.
type CloudControlGenerator struct {
	AWSService
	types []cloudControlType
}

func newCloudControlGenerator(service string) *CloudControlGenerator {
	return &CloudControlGenerator{types: cloudControlServices[service]}
}

func (g *CloudControlGenerator) InitResources() error {
	config, err := g.generateConfig()
	if err != nil {
		return err
	}
	client := cloudcontrol.NewFromConfig(config)
	for _, t := range g.types {
		if err := g.list(g.Context(), client, t); err != nil {
			return fmt.Errorf("%s: %w", t.CloudFormation, err)
		}
	}
	return nil
}

// list adds the resources of one type, if the region has the type.
func (g *CloudControlGenerator) list(ctx context.Context, client cloudcontrol.ListResourcesAPIClient, t cloudControlType) error {
	p := cloudcontrol.NewListResourcesPaginator(client, &cloudcontrol.ListResourcesInput{TypeName: aws.String(t.CloudFormation)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		var notFound *cctypes.TypeNotFoundException
		var unsupported *cctypes.UnsupportedActionException
		if errors.As(err, &notFound) || errors.As(err, &unsupported) {
			// Not every region offers every type.
			return nil
		}
		if err != nil {
			return err
		}
		for _, r := range page.ResourceDescriptions {
			id := aws.ToString(r.Identifier)
			if id == "" {
				continue
			}
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(id, cloudControlName(id), t.Terraform, "aws"))
		}
	}
	return nil
}

// cloudControlName names a resource after its identifier: an ARN's
// resource name, or the identifier itself.
func cloudControlName(id string) string {
	if strings.HasPrefix(id, "arn:") {
		return id[strings.LastIndexAny(id, "/:")+1:]
	}
	return id
}
