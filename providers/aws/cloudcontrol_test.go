// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
)

func TestCloudControlGenerator(t *testing.T) {
	var listed []string
	useFakeAPI(t, func(call apiCall) string {
		var in struct{ TypeName, NextToken string }
		if call.Op != "ListResources" || json.Unmarshal([]byte(call.Body), &in) != nil {
			t.Errorf("unexpected call %+v", call)
			return `{"__type":"ValidationException","Message":"unexpected"}`
		}
		listed = append(listed, in.TypeName)
		switch in.TypeName {
		case "AWS::Events::EventBus":
			if in.NextToken == "" {
				return `{"TypeName":"AWS::Events::EventBus","ResourceDescriptions":[{"Identifier":"default"}],"NextToken":"2"}`
			}
			return `{"TypeName":"AWS::Events::EventBus","ResourceDescriptions":[{"Identifier":"orders"}]}`
		case "AWS::Events::Archive":
			// Not offered in this region.
			return `{"__type":"TypeNotFoundException","Message":"type not found"}`
		case "AWS::Events::Connection":
			return `{"ResourceDescriptions":[{"Identifier":"crm"}]}`
		}
		return `{"ResourceDescriptions":[]}`
	})
	g := newCloudControlGenerator("eventbridge")

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_cloudwatch_event_bus", "default", "orders")
	assertIDs(t, g.Resources, "aws_cloudwatch_event_connection", "crm")
	if want := []string{"AWS::Events::ApiDestination", "AWS::Events::Archive", "AWS::Events::Connection", "AWS::Events::EventBus", "AWS::Events::EventBus"}; !reflect.DeepEqual(listed, want) {
		t.Errorf("listed %v, want %v", listed, want)
	}
}

func TestCloudControlGeneratorFails(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return `{"__type":"AccessDeniedException","Message":"not allowed"}`
	})
	g := newCloudControlGenerator("athena")

	err := g.InitResources()
	if err == nil || !strings.Contains(err.Error(), "AWS::Athena::WorkGroup") {
		t.Errorf("want an error naming the type, got %v", err)
	}
}

// Every service listed through Cloud Control is a service of the provider,
// named apart from the service listers, and imports each Terraform type
// once.
func TestCloudControlServices(t *testing.T) {
	supported := (&AWSProvider{}).GetSupportedService()
	types := map[string]string{}
	for name, ts := range cloudControlServices {
		facade, ok := supported[name].(*AwsFacade)
		if !ok {
			t.Errorf("%s isn't a supported service", name)
			continue
		}
		if g, ok := facade.service.(*CloudControlGenerator); !ok || len(g.types) != len(ts) || g.types[0].CloudFormation != ts[0].CloudFormation {
			t.Errorf("%s isn't listed through Cloud Control", name)
		}
		for _, typ := range ts {
			if other, ok := types[typ.Terraform]; ok {
				t.Errorf("%s is listed by %s and %s", typ.Terraform, other, name)
			}
			types[typ.Terraform] = name
			if !strings.HasPrefix(typ.CloudFormation, "AWS::") || !strings.HasPrefix(typ.Terraform, "aws_") {
				t.Errorf("%+v: not a CloudFormation and a Terraform type", typ)
			}
		}
	}
}

func TestCloudControlName(t *testing.T) {
	for id, want := range map[string]string{
		"arn:aws:codeartifact:us-east-1:123456789012:repository/shop/npm": "npm",
		"arn:aws:codeartifact:us-east-1:123456789012:domain/shop":         "shop",
		"rslvr-rr-0abc": "rslvr-rr-0abc",
		"primary":       "primary",
	} {
		if got := cloudControlName(id); got != want {
			t.Errorf("cloudControlName(%q): got %q, want %q", id, got, want)
		}
	}
}

func TestExcludedByDefaultCreatedByAWS(t *testing.T) {
	useFakeAPI(t, func(apiCall) string { return noStacks })
	p := &AWSProvider{}
	excluded, err := p.ExcludedByDefault(t.Context(), []terraformutils.Resource{
		terraformutils.NewSimpleResource("default", "default", "aws_cloudwatch_event_bus", "aws"),
		terraformutils.NewSimpleResource("orders", "orders", "aws_cloudwatch_event_bus", "aws"),
		terraformutils.NewSimpleResource("primary", "primary", "aws_athena_workgroup", "aws"),
		terraformutils.NewSimpleResource("rslvr-autodefined-rr-internet-resolver", "internet", "aws_route53_resolver_rule", "aws"),
		terraformutils.NewSimpleResource("rslvr-rr-0abc", "corp", "aws_route53_resolver_rule", "aws"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"aws_cloudwatch_event_bus default":                                 reasonCreatedByAWS,
		"aws_athena_workgroup primary":                                     reasonCreatedByAWS,
		"aws_route53_resolver_rule rslvr-autodefined-rr-internet-resolver": reasonCreatedByAWS,
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// CloudFormationSchemasEnv names a directory of the CloudFormation registry
// schemas, unzipped from
// https://schema.cloudformation.us-east-1.amazonaws.com/CloudformationSchema.zip,
// for TestCloudControlIdentifiersMatchSchemas.
const CloudFormationSchemasEnv = "INFRAHARVEST_CFN_SCHEMAS"

// Every Cloud Control type names the identifier its import IDs come from.
func TestCloudControlIdentifiers(t *testing.T) {
	for service, types := range cloudControlServices {
		for _, typ := range types {
			if typ.Identifier == "" || strings.Contains(typ.Identifier, "/") {
				t.Errorf("%s: %s: Identifier %q, want the primary identifier's property", service, typ.CloudFormation, typ.Identifier)
			}
		}
	}
}

// The primary identifier of each Cloud Control type, in the CloudFormation
// schemas, is the one property its Identifier names: Cloud Control's
// identifiers stay the Terraform import IDs. The cloudcontrol workflow runs
// it against the published schemas every week.
func TestCloudControlIdentifiersMatchSchemas(t *testing.T) {
	dir := os.Getenv(CloudFormationSchemasEnv)
	if dir == "" {
		t.Skip(CloudFormationSchemasEnv + " is not set")
	}
	for _, types := range cloudControlServices {
		for _, typ := range types {
			// AWS::Events::EventBus is in aws-events-eventbus.json.
			name := strings.ToLower(strings.ReplaceAll(typ.CloudFormation, "::", "-")) + ".json"
			content, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Errorf("%s: %v", typ.CloudFormation, err)
				continue
			}
			var schema struct {
				PrimaryIdentifier []string `json:"primaryIdentifier"`
			}
			if err := json.Unmarshal(content, &schema); err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			want := []string{"/properties/" + typ.Identifier}
			if !reflect.DeepEqual(schema.PrimaryIdentifier, want) {
				t.Errorf("%s: primary identifier %v, want %v: check the Terraform import ID", typ.CloudFormation, schema.PrimaryIdentifier, want)
			}
		}
	}
}
