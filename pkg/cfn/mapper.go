// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import "strings"

// Mapper names the Terraform side of a template: the resource type a
// CloudFormation type becomes, the argument or block each property
// becomes, and the attribute Ref and Fn::GetAtt read. Convert asks it about
// every resource, property and reference, so a caller with better
// knowledge than the built-in table, such as a provider schema, plugs it
// in through Options.Mapper; wrapping DefaultMapper and falling back to it
// keeps the built-in names for what the caller's table leaves out.
//
// Each method returns false when it has no answer. Convert then leaves the
// thing out of the output, with a diagnostic and a comment that keeps what
// the template said: a mapper should say false, not guess, when a guess
// would be wrong more often than right. A guess it is fairly sure of is
// returned with Guessed set, which Convert reports as information, so that
// a person checks it.
//
// A Mapper must be safe to call from several goroutines if the caller
// converts several templates at once; the converter itself calls it from
// one.
type Mapper interface {
	// ResourceType returns the Terraform resource type for a
	// CloudFormation resource type, such as aws_vpc for AWS::EC2::VPC.
	ResourceType(cfnType string) (Mapped, bool)
	// Property returns what a property of a cfnType resource becomes.
	// path is the property's path from the resource's Properties:
	// ["BucketName"] for a top-level property, ["Environment",
	// "Variables"] for a property nested in another. List elements don't
	// add to the path: the properties of each SecurityGroupIngress rule
	// are at ["SecurityGroupIngress", "IpProtocol"] and so on.
	Property(cfnType string, path []string) (Property, bool)
	// Ref returns the attribute of the Terraform resource that Ref to a
	// cfnType resource returns, such as id for AWS::EC2::VPC or arn for
	// AWS::SNS::Topic.
	Ref(cfnType string) (Mapped, bool)
	// Attribute returns the Terraform attribute that Fn::GetAtt reads,
	// such as arn for an AWS::S3::Bucket's Arn. attribute is as the
	// template writes it, dots and all (Endpoint.Address).
	Attribute(cfnType, attribute string) (Mapped, bool)
}

// Mapped is a Terraform name a Mapper returns. For Ref and Attribute,
// Name is an attribute path relative to the resource, such as "arn" or
// "endpoint[0].address".
type Mapped struct {
	Name string
	// Guessed is set when the mapper derived the name from the
	// CloudFormation one rather than knowing it.
	Guessed bool
	// Sensitive is set for an attribute the provider marks sensitive, such
	// as an SSM parameter's value: an output that reads it must be
	// sensitive too, or Terraform refuses the plan.
	Sensitive bool
}

// PropertyKind says how a property's value is written.
type PropertyKind int

const (
	// KindAttribute writes the property as an argument whose value is an
	// expression. The keys of a mapping in the value are properties too, at
	// the path one deeper, and go through Property in turn.
	KindAttribute PropertyKind = iota
	// KindBlock writes the property as a nested block named Name: one
	// block for a mapping, one block per element for a list of mappings.
	// The block's contents are properties at the path one deeper.
	KindBlock
	// KindMap writes the property as an argument whose value is a map
	// with its keys as written, such as a Lambda function's environment
	// variables.
	KindMap
	// KindTags writes a list of {Key, Value} mappings as a map from each
	// Key to its Value, as the AWS provider's tags argument takes them. A
	// value that is already a mapping is written as a map.
	KindTags
	// KindJSON writes a mapping or list as jsonencode() of it, with its
	// keys as written, such as an IAM policy document. A string, which
	// CloudFormation also accepts as a policy, is written as it is.
	KindJSON
	// KindOmit leaves the property out on purpose; Reason says why, and
	// Convert reports it as a warning, with the value as written: what it
	// sets doesn't take effect until a person writes it as the provider
	// takes it.
	KindOmit
)

// Property is what a property becomes.
type Property struct {
	// Name is the Terraform argument or block name.
	Name string
	Kind PropertyKind
	// List is set when Terraform takes a list where CloudFormation takes
	// one value, such as a security group rule's cidr_blocks for its
	// CidrIp: a single value is written as a one-element list.
	List bool
	// Guessed is set when the mapper derived Name from the property name.
	Guessed bool
	// Reason says why a KindOmit property is left out, such as "the AWS
	// provider manages it with the aws_s3_bucket_versioning resource".
	Reason string
}

// DefaultMapper returns the built-in mapper. It knows the names of the
// common AWS resource types listed in its table (VPC networking, S3, IAM,
// Lambda, SNS, SQS, KMS, CloudWatch Logs, DynamoDB, RDS, ECR, ECS, Route
// 53, load balancers, Secrets Manager, SSM parameters and EventBridge
// rules), and derives the rest: AWS::Service::Thing becomes
// aws_service_thing, a property its snake_case name (a name ending in
// PolicyDocument a JSON policy, a top-level Tags a tags map), Ref the
// resource's id, and an attribute its snake_case name, all marked Guessed.
// It has no answer for types outside the AWS namespace, for SAM's
// AWS::Serverless types, or for AWS::CloudFormation's own.
func DefaultMapper() Mapper {
	return defaultMapper{}
}

type defaultMapper struct{}

// guessable reports whether the default mapper derives a type's names: an
// AWS type that isn't SAM's or CloudFormation's own.
func guessable(cfnType string) bool {
	parts := strings.Split(cfnType, "::")
	return len(parts) == 3 && parts[0] == "AWS" && parts[1] != "Serverless" && parts[1] != "CloudFormation" && parts[1] != "" && parts[2] != ""
}

func (defaultMapper) ResourceType(cfnType string) (Mapped, bool) {
	if e, ok := builtinTypes[cfnType]; ok {
		return Mapped{Name: e.tf}, true
	}
	if !guessable(cfnType) {
		return Mapped{}, false
	}
	parts := strings.Split(cfnType, "::")
	return Mapped{Name: "aws_" + snake(parts[1]) + "_" + snake(parts[2]), Guessed: true}, true
}

func (defaultMapper) Property(cfnType string, path []string) (Property, bool) {
	if len(path) == 0 {
		return Property{}, false
	}
	if e, ok := builtinTypes[cfnType]; ok {
		if p, ok := e.props[strings.Join(path, ".")]; ok {
			return p, true
		}
	} else if !guessable(cfnType) {
		return Property{}, false
	}
	last := path[len(path)-1]
	switch {
	case last == "Tags" && len(path) == 1:
		return Property{Name: "tags", Kind: KindTags}, true
	case strings.HasSuffix(last, "PolicyDocument"):
		return Property{Name: snake(strings.TrimSuffix(last, "Document")), Kind: KindJSON, Guessed: true}, true
	}
	return Property{Name: ident(last), Guessed: true}, true
}

func (defaultMapper) Ref(cfnType string) (Mapped, bool) {
	if e, ok := builtinTypes[cfnType]; ok {
		if e.ref == "" {
			return Mapped{Name: "id"}, true
		}
		return Mapped{Name: e.ref}, true
	}
	if !guessable(cfnType) {
		return Mapped{}, false
	}
	return Mapped{Name: "id", Guessed: true}, true
}

func (defaultMapper) Attribute(cfnType, attribute string) (Mapped, bool) {
	e, known := builtinTypes[cfnType]
	if known {
		if a, ok := e.attrs[attribute]; ok {
			return Mapped{Name: a}, true
		}
	} else if !guessable(cfnType) {
		return Mapped{}, false
	}
	if attribute == "Arn" {
		return Mapped{Name: "arn", Guessed: !known}, true
	}
	parts := strings.Split(attribute, ".")
	for i, p := range parts {
		parts[i] = ident(p)
	}
	return Mapped{Name: strings.Join(parts, "_"), Guessed: true}, true
}
