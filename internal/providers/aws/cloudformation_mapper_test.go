// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/pkg/cfn"
)

func testSchemas() *tfjson.ProviderSchemas {
	optional := &tfjson.SchemaAttribute{Optional: true}
	computed := &tfjson.SchemaAttribute{Computed: true}
	return &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
			"aws_s3_bucket": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
				"id": computed, "arn": computed, "bucket": optional, "tags": optional, "bucket_domain_name": computed,
			}}},
			"aws_security_group": {Block: &tfjson.SchemaBlock{
				Attributes: map[string]*tfjson.SchemaAttribute{"id": computed, "description": optional, "vpc_id": optional},
				NestedBlocks: map[string]*tfjson.SchemaBlockType{"ingress": {Block: &tfjson.SchemaBlock{
					Attributes: map[string]*tfjson.SchemaAttribute{"protocol": optional, "from_port": optional},
				}}},
			}},
		}},
	}}
}

func TestStackMapper(t *testing.T) {
	m := newStackMapper(map[string][]string{
		"AWS::S3::Bucket":         {"aws_s3_bucket"},
		"AWS::EC2::SecurityGroup": {"aws_security_group"},
		"AWS::SNS::Topic":         {"aws_sns_topic"},
	}, providerResourceSchemas(testSchemas(), "aws"))

	if got, ok := m.ResourceType("AWS::S3::Bucket"); !ok || got != (cfn.Mapped{Name: "aws_s3_bucket"}) {
		t.Errorf("ResourceType = %v, %v", got, ok)
	}
	// The schema has no aws_sns_topic.
	if _, ok := m.ResourceType("AWS::SNS::Topic"); ok {
		t.Error("a type the schema lacks has a Terraform type")
	}
	properties := []struct {
		cfnType string
		path    []string
		want    cfn.Property
		ok      bool
	}{
		{"AWS::S3::Bucket", []string{"BucketName"}, cfn.Property{Name: "bucket"}, true},
		{"AWS::S3::Bucket", []string{"Tags"}, cfn.Property{Name: "tags", Kind: cfn.KindTags}, true},
		// Derived from the name, but the schema has no such argument.
		{"AWS::S3::Bucket", []string{"MetricsConfigurations"}, cfn.Property{}, false},
		{"AWS::S3::Bucket", []string{"VersioningConfiguration"}, cfn.Property{Kind: cfn.KindOmit, Reason: "the AWS provider manages it with a separate aws_s3_bucket_versioning resource"}, true},
		{"AWS::EC2::SecurityGroup", []string{"SecurityGroupIngress", "IpProtocol"}, cfn.Property{Name: "protocol"}, true},
		{"AWS::EC2::SecurityGroup", []string{"SecurityGroupIngress"}, cfn.Property{Name: "ingress", Kind: cfn.KindBlock}, true},
	}
	for _, tt := range properties {
		got, ok := m.Property(tt.cfnType, tt.path)
		if ok != tt.ok || got != tt.want {
			t.Errorf("Property(%s, %v) = %+v, %v; want %+v, %v", tt.cfnType, tt.path, got, ok, tt.want, tt.ok)
		}
	}
	if got, ok := m.Attribute("AWS::S3::Bucket", "Arn"); !ok || got.Name != "arn" || got.Guessed {
		t.Errorf("Attribute(Arn) = %v, %v", got, ok)
	}
	if got, ok := m.Attribute("AWS::S3::Bucket", "DomainName"); !ok || got.Name != "bucket_domain_name" {
		t.Errorf("Attribute(DomainName) = %v, %v", got, ok)
	}
	if _, ok := m.Attribute("AWS::S3::Bucket", "WebsiteURL"); ok {
		t.Error("an attribute the schema lacks has a name")
	}
	if got, ok := m.Ref("AWS::EC2::SecurityGroup"); !ok || got.Name != "id" {
		t.Errorf("Ref = %v, %v", got, ok)
	}
}
