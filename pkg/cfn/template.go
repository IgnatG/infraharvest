// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

// Template is a parsed CloudFormation template. Each section keeps the
// order the template lists its entries in. Sections the converter has no
// Terraform equivalent for (Metadata, Rules and anything else) are kept as
// written, so that a caller can still read them and Convert can report them.
type Template struct {
	// FormatVersion is AWSTemplateFormatVersion, such as "2010-09-09".
	FormatVersion string
	Description   string
	// Transform lists the macros CloudFormation runs on the template before
	// it deploys it, such as AWS::Serverless-2016-10-31.
	Transform  []string
	Parameters []*Parameter
	Mappings   []*Mapping
	Conditions []*Condition
	Resources  []*Resource
	Outputs    []*Output
	// Metadata and Rules are the sections as written, or nil.
	Metadata Node
	Rules    Node
	// Loops are the Fn::ForEach entries of the Resources, Conditions and
	// Outputs sections (the AWS::LanguageExtensions transform), keyed by
	// their path, such as "Resources.Fn::ForEach::Topics".
	Loops Map
	// Other holds any other top-level section, as written.
	Other Map
}

// Parameter is an entry of the Parameters section.
type Parameter struct {
	Name string
	// Type is the parameter type as written, such as String, Number,
	// List<Number>, CommaDelimitedList, AWS::EC2::VPC::Id or
	// AWS::SSM::Parameter::Value<String>.
	Type        string
	Description string
	// Default is the default value as written, or nil.
	Default       Node
	AllowedValues []Node
	// AllowedPattern is a regular expression the whole value must match.
	AllowedPattern string
	// MinLength, MaxLength, MinValue and MaxValue are empty when the
	// template doesn't set them.
	MinLength             Number
	MaxLength             Number
	MinValue              Number
	MaxValue              Number
	ConstraintDescription string
	NoEcho                bool
}

// Mapping is an entry of the Mappings section: a two-level lookup table,
// whose top-level keys map to second-level keys that map to values.
type Mapping struct {
	Name  string
	Value Map
}

// Condition is an entry of the Conditions section.
type Condition struct {
	Name string
	// Expr is the condition: a Func such as Fn::Equals, or a Map with the
	// single key Condition naming another condition.
	Expr Node
}

// Resource is an entry of the Resources section.
type Resource struct {
	LogicalID  string
	Type       string
	Properties Map
	DependsOn  []string
	// Condition names the condition that decides whether CloudFormation
	// creates the resource, or is empty.
	Condition string
	// DeletionPolicy and UpdateReplacePolicy are as written (usually a
	// String such as Retain), or nil.
	DeletionPolicy      Node
	UpdateReplacePolicy Node
	Metadata            Node
	// Other holds the resource's other attributes, such as CreationPolicy
	// and UpdatePolicy, as written.
	Other Map
}

// Output is an entry of the Outputs section.
type Output struct {
	Name        string
	Description string
	Value       Node
	// Export is the name the output is exported under, or nil.
	Export    Node
	Condition string
}

// Parameter returns the parameter called name, or nil.
func (t *Template) Parameter(name string) *Parameter {
	for _, p := range t.Parameters {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// Resource returns the resource with the logical ID id, or nil.
func (t *Template) Resource(id string) *Resource {
	for _, r := range t.Resources {
		if r.LogicalID == id {
			return r
		}
	}
	return nil
}
