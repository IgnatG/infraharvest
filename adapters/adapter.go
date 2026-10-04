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
)

// Adapter maps a cluster of resources onto a call of one module version.
type Adapter struct {
	// Source is the module's registry address, and Version its exact
	// version: an adapter is tested against that version only.
	Source  string
	Version string
	// Anchor is the type of the resource a cluster is built around, and
	// Members the types of the resources that join it by referring to
	// the anchor, at most one of each.
	Anchor  string
	Members []string
	// Inputs are every argument Map can set, and Outputs every module
	// output it can name, for the check against the module's interface.
	Inputs  []string
	Outputs []string
	// Map returns the module call for a cluster, or a *Decline.
	Map func(Cluster) (*Call, error)
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
	// Arguments are the call's arguments, in order.
	Arguments []Argument
	// Addresses has each resource's address inside the module, such as
	// aws_s3_bucket.this[0], by its address in the root.
	Addresses map[string]string
	// Outputs has, by resource address and attribute, the module output
	// that has the attribute's value: references to the resources become
	// references to the outputs.
	Outputs map[string]map[string]string
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
