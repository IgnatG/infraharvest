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

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// newCloudControlLister returns a lister of the built-in catalogue's
// Cloud Control service.
func newCloudControlLister(service string) *CloudControlLister {
	return &CloudControlLister{types: cloudControlServices()[service]}
}

func TestCloudControlLister(t *testing.T) {
	t.Parallel()
	var listed []string
	ctx := useFakeAPI(t, func(call apiCall) string {
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
	g := newCloudControlLister("eventbridge")

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertIDs(t, resources, "aws_cloudwatch_event_bus", "default", "orders")
	assertIDs(t, resources, "aws_cloudwatch_event_connection", "crm")
	if want := []string{"AWS::Events::ApiDestination", "AWS::Events::Archive", "AWS::Events::EventBus", "AWS::Events::EventBus", "AWS::Events::Connection"}; !reflect.DeepEqual(listed, want) {
		t.Errorf("listed %v, want %v", listed, want)
	}
}

func TestCloudControlListerFails(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return `{"__type":"AccessDeniedException","Message":"not allowed"}`
	})
	g := newCloudControlLister("athena")

	_, err := g.List(ctx, harvest.Settings{})
	if err == nil || !strings.Contains(err.Error(), "AWS::Athena::WorkGroup") || harvest.IsPartial(err) {
		t.Errorf("want the service failed, with an error naming the type, got %v", err)
	}
}

// A type that fails, as one an SCP denies, doesn't stop the service's other
// types: what they list is kept, and the listing reads as partial, naming
// the type. A type Cloud Control has no list handler for lists nothing.
func TestCloudControlListerListsTheOtherTypes(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		var in struct{ TypeName string }
		if err := json.Unmarshal([]byte(call.Body), &in); err != nil {
			t.Fatal(err)
		}
		switch in.TypeName {
		case "AWS::Events::ApiDestination":
			return `{"__type":"AccessDeniedException","Message":"denied by an SCP"}`
		case "AWS::Events::Archive":
			return `{"__type":"UnsupportedActionException","Message":"no list handler"}`
		case "AWS::Events::Connection":
			return `{"ResourceDescriptions":[{"Identifier":"crm"}]}`
		}
		return `{"ResourceDescriptions":[]}`
	})
	g := newCloudControlLister("eventbridge")

	resources, err := g.List(ctx, harvest.Settings{})

	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "AWS::Events::ApiDestination") || strings.Contains(err.Error(), "AWS::Events::Archive") {
		t.Errorf("want a partial error naming the denied type only, got %v", err)
	}
	assertIDs(t, resources, "aws_cloudwatch_event_connection", "crm")
}

// Every service listed through Cloud Control is a service of the provider,
// named apart from the service listers, and imports each Terraform type
// once.
func TestCloudControlServices(t *testing.T) {
	t.Parallel()
	supported := (&Provider{}).Services()
	types := map[string]string{}
	for name, ts := range cloudControlServices() {
		guard, ok := supported[name].(*guardedLister)
		if !ok {
			t.Errorf("%s isn't a supported service", name)
			continue
		}
		if g, ok := guard.lister.(*CloudControlLister); !ok || len(g.types) != len(ts) || g.types[0].CloudFormation != ts[0].CloudFormation {
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

// A --catalogue overlay's cloud_control entry adds its type, and its
// service, once SetCatalogue gives the provider the overlay: Services has
// the service, and Select makes its Cloud Control lister.
func TestCloudControlServicesOfTheOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	overlay := "provider: aws\nservice: ivs\ntypes:\n  aws_ivs_channel:\n    cloud_control: { type: \"AWS::IVS::Channel\", identifier: Arn }\n"
	if err := os.WriteFile(filepath.Join(dir, "aws.yaml"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := catalogue.Load("aws", dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{}
	if _, ok := p.Services()["ivs"]; ok {
		t.Fatal("before SetCatalogue, want only the built-in services")
	}
	p.SetCatalogue(cat)

	if _, ok := p.Services()["ivs"]; !ok {
		t.Error("the overlay's service isn't among the services")
	}
	lister, err := p.Lister("ivs")
	if err != nil {
		t.Fatal(err)
	}
	guard, ok := lister.(*guardedLister)
	if !ok {
		t.Fatalf("got %T, want a guardedLister", lister)
	}
	want := []cloudControlType{{CloudFormation: "AWS::IVS::Channel", Identifier: "Arn", Terraform: "aws_ivs_channel"}}
	if g, ok := guard.lister.(*CloudControlLister); !ok || !reflect.DeepEqual(g.types, want) {
		t.Errorf("got %+v, want a Cloud Control lister of %+v", guard.lister, want)
	}
	if len(p.cloudControl()["eventbridge"]) != len(cloudControlServices()["eventbridge"]) {
		t.Error("the built-in Cloud Control services aren't kept")
	}
}

func TestCloudControlName(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]string{
		"arn:aws:codeartifact:us-east-1:123456789012:repository/shop/npm":                            "npm",
		"arn:aws:codeartifact:us-east-1:123456789012:domain/shop":                                    "shop",
		"arn:aws:kafka:us-east-1:123456789012:cluster/orders/0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e-4": "0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e-4",
		"rslvr-rr-0abc": "rslvr-rr-0abc",
		"primary":       "primary",
	} {
		if got := cloudControlName(id); got != want {
			t.Errorf("cloudControlName(%q): got %q, want %q", id, got, want)
		}
	}
}

// CloudFormationSchemasEnv names a directory of the CloudFormation registry
// schemas, unzipped from
// https://schema.cloudformation.us-east-1.amazonaws.com/CloudformationSchema.zip,
// for TestCloudControlIdentifiersMatchSchemas.
const CloudFormationSchemasEnv = "INFRAHARVEST_CFN_SCHEMAS"

// Every Cloud Control type names the identifier its import IDs come from.
func TestCloudControlIdentifiers(t *testing.T) {
	t.Parallel()
	for service, types := range cloudControlServices() {
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
	t.Parallel()
	dir := os.Getenv(CloudFormationSchemasEnv)
	if dir == "" {
		t.Skip(CloudFormationSchemasEnv + " is not set")
	}
	for _, types := range cloudControlServices() {
		for _, typ := range types {
			// AWS::Events::EventBus is in aws-events-eventbus.json.
			name := strings.ToLower(strings.ReplaceAll(typ.CloudFormation, "::", "-")) + ".json"
			content, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Errorf("%s: %v", typ.CloudFormation, err)
				continue
			}
			var schema struct {
				PrimaryIdentifier []string                   `json:"primaryIdentifier"`
				Handlers          map[string]json.RawMessage `json:"handlers"`
			}
			if err := json.Unmarshal(content, &schema); err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			want := []string{"/properties/" + typ.Identifier}
			if !reflect.DeepEqual(schema.PrimaryIdentifier, want) {
				t.Errorf("%s: primary identifier %v, want %v: check the Terraform import ID", typ.CloudFormation, schema.PrimaryIdentifier, want)
			}
			// Without a list handler, Cloud Control can't list the type,
			// and the service would list nothing of it in every region.
			if _, ok := schema.Handlers["list"]; !ok {
				t.Errorf("%s: no list handler, so Cloud Control can't list it", typ.CloudFormation)
			}
		}
	}
}
