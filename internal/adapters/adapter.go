// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package adapters maps clusters of imported resources onto calls of
// curated public modules, such as terraform-aws-modules/s3-bucket/aws.
// An adapter is data (the module, its exact version, which resources it
// takes) and a function that turns the resources' generated configuration
// into the module's arguments. It declines what the module can't express:
// the engine then falls back to a generated local module, which is always
// exact. The engine keeps a module call only if the plan stays the same.
package adapters

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Adapter maps a cluster of resources onto a call of one module version.
type Adapter struct {
	// Source is the module's registry address, and Version its exact
	// version: an adapter is tested against that version only.
	Source  string
	Version string
	// Anchor is the type of the resource a cluster is built around, and
	// Members the types of the resources that join it by referring to
	// the anchor: at most one of each, but for the types in Repeated.
	// Members of the types in Through join through the cluster's other
	// resources instead: by referring to one, or by one referring to them,
	// such as a route table association through its subnet, or a NAT
	// gateway's Elastic IP through the gateway.
	Anchor   string
	Members  []string
	Repeated []string
	Through  []string
	// Group, if set, joins resources of the anchor's type with the same
	// key into one cluster, the first in address order its anchor, such as
	// the IAM grants in a project, which refer to nothing they share. A
	// resource it gives no key anchors no cluster and joins none; the
	// anchor's type must be in Members and Repeated.
	Group func(Resource) (string, bool)
	// Inputs are every argument Map can set, and Outputs every module
	// output it can name, for the check against the module's interface.
	Inputs  []string
	Outputs []string
	// Targets are the addresses inside the module, without instance
	// keys, that Map puts resources at or imports them to, such as
	// aws_s3_bucket.this or module.vpc.google_compute_network.network: a
	// release that no longer declares one doesn't fit the adapter.
	Targets []string
	// Providers are the providers the module uses that may not be the
	// root's own, such as google and google-beta, of which google --beta's
	// root has only google-beta: the root configures each it doesn't
	// have as its own provider, for the same project or subscription.
	// Generic reads them from the module itself.
	Providers []string
	// Map returns the module call for a cluster, or a *Decline.
	Map func(Cluster) (*Call, error)
	// Defaults are inputs' defaults, as the module's variables declare
	// them, or values the module passes on as a null the provider takes
	// as that value: a call leaves out the arguments Map sets to them,
	// as a person calling the module would (see IsDefault).
	Defaults map[string]cty.Value
	// Inverse maps, for drift, an attribute of a resource inside the
	// module, by its target and its path, such as aws_iam_role.this.tags
	// or google_storage_bucket.bucket.versioning[0].enabled, onto the
	// input path that sets it (see ParseInputPath), such as tags: a change
	// in the cloud to the attribute, or to a key or an element in it, is
	// taken into the call by setting the input, or the same key or
	// element in it. [each.key] stands for the instance key of a resource
	// the module makes with for_each over the input, such as
	// policies[each.key]. Only attributes the module sets from an input
	// as it is, or from the input whose keys win in a merge, such as a
	// VPC's vpc_tags, have an entry: a change to another is a hand edit.
	// Each entry is checked against the module's interface (see
	// moduleinterface.Check).
	Inverse map[string]string
}

// Cluster is an anchor resource and the resources that refer to it.
type Cluster struct {
	Anchor  Resource
	Members []Resource // in address order
}

// Resource is a resource's generated configuration.
type Resource struct {
	Type, Name string
	Body       *hclwrite.Body
}

// Address is the resource's address in the root.
func (r Resource) Address() string {
	return r.Type + "." + r.Name
}

// Call is what a module call needs besides its source and version.
type Call struct {
	// Name, if set, is what the call is named, rather than its anchor's
	// name: the anchor of a group is only its first resource in address
	// order, such as one of a project's IAM grants.
	Name string
	// Arguments are the call's arguments, in order.
	Arguments []Argument
	// Addresses has each resource's address inside the module, such as
	// aws_s3_bucket.this[0], by its address in the root.
	Addresses map[string]string
	// Outputs has, by resource address and attribute, the module output
	// that has the attribute's value, or a path into one, such as
	// subnets["eu/app"].self_link: references to the resources become
	// references to the outputs.
	Outputs map[string]map[string]string
	// Imports are resources the module manages separately that the root
	// has inside the cluster's resources, such as a route the root keeps
	// in its route table's route blocks.
	Imports []Import
	// Left are the addresses of members the module doesn't manage, which
	// stay in the root, such as a VPC's main route table.
	Left []string
}

// Import imports a resource the root has no resource block for into the
// module.
type Import struct {
	// To is the resource's address inside the module, such as
	// aws_route.public_internet_gateway[0].
	To string
	// Of is the address in the root of the cluster resource whose import
	// ID the resource's starts with, and Suffix the rest of it, such as
	// the route table's and _0.0.0.0/0 for a route.
	Of, Suffix string
}

// Argument is a module call argument.
type Argument struct {
	Name  string
	Value hclwrite.Tokens
}

// Set adds an argument to the call.
func (c *Call) Set(name string, value hclwrite.Tokens) {
	c.Arguments = append(c.Arguments, Argument{Name: name, Value: value})
}

// Decline is why an adapter doesn't map a cluster.
type Decline struct {
	Reason string
}

func (d *Decline) Error() string {
	return d.Reason
}

// Declinef returns a *Decline.
func Declinef(format string, args ...any) error {
	return &Decline{Reason: fmt.Sprintf(format, args...)}
}

// IsDecline reports whether err is a *Decline.
func IsDecline(err error) bool {
	var d *Decline
	return errors.As(err, &d)
}

// Reader reads a resource's or a nested block's configuration, and records
// what was read, so that an adapter can decline anything it didn't map:
// a setting the module can't express must never be dropped silently.
type Reader struct {
	path     string
	body     *hclwrite.Body
	read     map[string]bool
	children []*Reader
}

// Read starts reading body; path names it in decline reasons.
func Read(path string, body *hclwrite.Body) *Reader {
	return &Reader{path: path, body: body, read: map[string]bool{}}
}

// Attr returns an argument's expression, or false if the argument is
// absent or null.
func (r *Reader) Attr(name string) (hclwrite.Tokens, bool) {
	r.read[name] = true
	attr := r.body.GetAttribute(name)
	if attr == nil {
		return nil, false
	}
	tokens := attr.Expr().BuildTokens(nil)
	if isNull(tokens) {
		return nil, false
	}
	return tokens, true
}

// Ignore marks arguments as read without reading them.
func (r *Reader) Ignore(names ...string) {
	for _, name := range names {
		r.read[name] = true
	}
}

// Blocks returns readers of the nested blocks of a type.
func (r *Reader) Blocks(typ string) []*Reader {
	r.read[typ] = true
	var readers []*Reader
	for _, b := range r.body.Blocks() {
		if b.Type() == typ {
			child := Read(r.path+"."+typ, b.Body())
			r.children = append(r.children, child)
			readers = append(readers, child)
		}
	}
	return readers
}

// Block returns a reader of the nested block of a type, nil if there is
// none, or a *Decline if there are several.
func (r *Reader) Block(typ string) (*Reader, error) {
	blocks := r.Blocks(typ)
	switch len(blocks) {
	case 0:
		return nil, nil
	case 1:
		return blocks[0], nil
	}
	return nil, Declinef("%s has %d %s blocks", r.path, len(blocks), typ)
}

// RefersTo reports whether an argument is a reference to resource.
func (r *Reader) RefersTo(name string, resource Resource) bool {
	r.read[name] = true
	attr := r.body.GetAttribute(name)
	if attr == nil {
		return false
	}
	expr, diags := hclsyntax.ParseExpression(attr.Expr().BuildTokens(nil).Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return false
	}
	return refersTo(expr, resource)
}

// refersTo reports whether an expression is a reference to resource:
// <type>.<name>, with or without more steps.
func refersTo(expr hclsyntax.Expression, resource Resource) bool {
	traversal, ok := expr.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(traversal.Traversal) < 2 || traversal.Traversal.RootName() != resource.Type {
		return false
	}
	step, ok := traversal.Traversal[1].(hcl.TraverseAttr)
	return ok && step.Name == resource.Name
}

// Done returns a *Decline naming the arguments and blocks, here and in the
// blocks read, that are set but weren't read.
func (r *Reader) Done() error {
	unread := r.unread()
	if len(unread) == 0 {
		return nil
	}
	return Declinef("the module can't set %s", strings.Join(unread, ", "))
}

func (r *Reader) unread() []string {
	var names []string
	for name, attr := range r.body.Attributes() {
		if !r.read[name] && !isNull(attr.Expr().BuildTokens(nil)) {
			names = append(names, r.path+"."+name)
		}
	}
	for _, b := range r.body.Blocks() {
		if !r.read[b.Type()] {
			names = append(names, r.path+"."+b.Type())
		}
	}
	for _, child := range r.children {
		names = append(names, child.unread()...)
	}
	sort.Strings(names)
	return names
}

func isNull(tokens hclwrite.Tokens) bool {
	return string(bytes.TrimSpace(tokens.Bytes())) == "null"
}

// Object builds an object expression, such as a module argument of an
// object type, from a block's arguments.
type Object struct {
	attrs []hclwrite.ObjectAttrTokens
}

// Set adds an attribute.
func (o *Object) Set(name string, value hclwrite.Tokens) {
	o.attrs = append(o.attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForIdentifier(name), Value: value})
}

// Copy sets each argument of r that is set, under the same name.
func (o *Object) Copy(r *Reader, names ...string) {
	for _, name := range names {
		if value, ok := r.Attr(name); ok {
			o.Set(name, value)
		}
	}
}

// Empty reports whether the object has no attributes.
func (o *Object) Empty() bool {
	return len(o.attrs) == 0
}

// Tokens returns the object's expression.
func (o *Object) Tokens() hclwrite.Tokens {
	return hclwrite.TokensForObject(o.attrs)
}

// Setter is what Require and CopyAs set module inputs on: a *Call, or an
// *Object for an input of an object type.
type Setter interface {
	Set(name string, value hclwrite.Tokens)
}

// Pair names a resource argument and the module input that takes its
// value.
type Pair struct {
	Arg, Input string
}

// Require sets each pair's input on to to its argument's value, in order,
// and returns a *Decline naming the first argument that is absent or null:
// one the module can't do without.
func (r *Reader) Require(to Setter, pairs ...Pair) error {
	for _, p := range pairs {
		value, ok := r.Attr(p.Arg)
		if !ok {
			return Declinef("%s has no %s", r.path, p.Arg)
		}
		to.Set(p.Input, value)
	}
	return nil
}

// CopyAs sets each pair's input on to to its argument's value, in order,
// for the arguments that are set.
func (r *Reader) CopyAs(to Setter, pairs ...Pair) {
	for _, p := range pairs {
		if value, ok := r.Attr(p.Arg); ok {
			to.Set(p.Input, value)
		}
	}
}
