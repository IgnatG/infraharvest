// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
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
