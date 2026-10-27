// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

// Template is a parsed CloudFormation template. Each section keeps the
// order the template lists its entries in. Sections the converter has no
// Terraform equivalent for (Metadata, Rules and anything else) are kept as
// written, so that a caller can still read them and Convert can report them.
//
// Parse never puts a nil entry in a section. A Template built by hand
// mustn't either: Convert and ConvertValue return an error for one.
type Template struct {
	// FormatVersion is AWSTemplateFormatVersion, such as "2010-09-09".
	FormatVersion string
	// Description is the template's Description, or empty.
	Description string
	// Transform lists the macros CloudFormation runs on the template before
	// it deploys it, such as AWS::Serverless-2016-10-31.
	Transform []string
	// Parameters are the entries of the Parameters section.
	Parameters []*Parameter
	// Mappings are the entries of the Mappings section.
	Mappings []*Mapping
	// Conditions are the entries of the Conditions section.
	Conditions []*Condition
	// Resources are the entries of the Resources section.
	Resources []*Resource
	// Outputs are the entries of the Outputs section.
	Outputs []*Output
	// Metadata is the Metadata section as written, or nil.
	Metadata Node
	// Rules is the Rules section as written, or nil.
	Rules Node
	// Loops are the Fn::ForEach entries of the Resources, Conditions and
	// Outputs sections (the AWS::LanguageExtensions transform), keyed by
	// their path, such as "Resources.Fn::ForEach::Topics".
	Loops Map
	// Other holds any other top-level section, as written.
	Other Map
}

// Parameter is an entry of the Parameters section.
type Parameter struct {
	// Name is the parameter's logical name.
	Name string
	// Type is the parameter type as written, such as String, Number,
	// List<Number>, CommaDelimitedList, AWS::EC2::VPC::Id or
	// AWS::SSM::Parameter::Value<String>.
	Type string
	// Description is the parameter's Description, or empty.
	Description string
	// Default is the default value as written, or nil.
	Default Node
	// AllowedValues are the values the parameter may take, as written, or
	// nil for any.
	AllowedValues []Node
	// AllowedPattern is a regular expression the whole value must match.
	AllowedPattern string
	// MinLength is the shortest a String value may be, or empty when the
	// template doesn't set it.
	MinLength Number
	// MaxLength is the longest a String value may be, or empty.
	MaxLength Number
	// MinValue is the smallest a Number value may be, or empty.
	MinValue Number
	// MaxValue is the largest a Number value may be, or empty.
	MaxValue Number
	// ConstraintDescription is the message CloudFormation shows when a
	// value breaks a constraint, or empty.
	ConstraintDescription string
	// NoEcho is set for a parameter whose value CloudFormation masks,
	// such as a password; its variable is sensitive.
	NoEcho bool
}

// Mapping is an entry of the Mappings section: a two-level lookup table,
// whose top-level keys map to second-level keys that map to values.
type Mapping struct {
	// Name is the mapping's logical name, which Fn::FindInMap names.
	Name string
	// Value is the table as written.
	Value Map
}

// Condition is an entry of the Conditions section.
type Condition struct {
	// Name is the condition's logical name.
	Name string
	// Expr is the condition: a Func such as Fn::Equals, or a Map with the
	// single key Condition naming another condition.
	Expr Node
}

// Resource is an entry of the Resources section.
type Resource struct {
	// LogicalID is the resource's logical ID, such as "Bucket".
	LogicalID string
	// Type is the resource type as written, such as AWS::S3::Bucket.
	Type string
	// Properties are the resource's properties as written, or nil.
	Properties Map
	// DependsOn lists the logical IDs of the resources this one depends
	// on, whether the template wrote one name or a list.
	DependsOn []string
	// Condition names the condition that decides whether CloudFormation
	// creates the resource, or is empty.
	Condition string
	// DeletionPolicy is as written (usually a String such as Retain), or
	// nil.
	DeletionPolicy Node
	// UpdateReplacePolicy is as written, like DeletionPolicy, or nil.
	UpdateReplacePolicy Node
	// Metadata is the resource's Metadata attribute as written, or nil.
	Metadata Node
	// Other holds the resource's other attributes, such as CreationPolicy
	// and UpdatePolicy, as written.
	Other Map
}

// Output is an entry of the Outputs section.
type Output struct {
	// Name is the output's logical name.
	Name string
	// Description is the output's Description, or empty.
	Description string
	// Value is the output's value as written.
	Value Node
	// Export is the name the output is exported under, or nil.
	Export Node
	// Condition names the condition that decides whether CloudFormation
	// creates the output, or is empty.
	Condition string
}

// Parameter returns the parameter called name, or nil. It skips nil
// entries, which only a Template built by hand can have.
func (t *Template) Parameter(name string) *Parameter {
	for _, p := range t.Parameters {
		if p != nil && p.Name == name {
			return p
		}
	}
	return nil
}

// Resource returns the resource with the logical ID id, or nil. Like
// Parameter, it skips nil entries.
func (t *Template) Resource(id string) *Resource {
	for _, r := range t.Resources {
		if r != nil && r.LogicalID == id {
			return r
		}
	}
	return nil
}
