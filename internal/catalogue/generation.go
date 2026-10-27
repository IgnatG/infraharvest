// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// What the engine reads of a type as it generates configuration: how its
// provider writes and names what the engine finds in it. The engine runs
// Terraform for any provider and tells none apart by name, so what it
// knows of one is here, as data. It reads it of the built-in catalogue
// (see ForType) wherever a root is generated, merged or drifts, whatever
// --catalogue overlays: an overlay can't set these fields (see
// builtinOnly).

// ForType returns the built-in catalogue of the provider whose type typ
// is, by the prefix the provider's local name gives its types (aws for
// aws_s3_bucket), empty for a provider without one.
func ForType(typ string) *Catalogue {
	provider, _, _ := strings.Cut(typ, "_")
	return MustBuiltin(provider)
}

// builtinOnly reports, by field, whether an entry sets one of the fields
// only the built-in catalogue takes: the engine reads them of the
// built-in catalogue alone (see ForType), so one an overlay set would
// change nothing.
var builtinOnly = map[string]func(*Entry) bool{
	"blocks_as_list":     func(e *Entry) bool { return e.BlocksAsList != nil },
	"container":          func(e *Entry) bool { return e.Container != nil },
	"derived_identity":   func(e *Entry) bool { return e.DerivedIdentity != nil },
	"module_references":  func(e *Entry) bool { return e.ModuleReferences != nil },
	"principal":          func(e *Entry) bool { return e.Principal != nil },
	"secret_when":        func(e *Entry) bool { return e.SecretWhen != nil },
	"unstable_id_suffix": func(e *Entry) bool { return e.UnstableIDSuffix != "" },
}

// everyType are the fields of builtinOnly that the entry for every type
// takes too, for each of the provider's types.
var everyType = map[string]bool{"unstable_id_suffix": true}

// checkBuiltinOnly reports the fields an overlay's entry sets that only
// the built-in catalogue takes.
func checkBuiltinOnly(overlay *Entry) []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(builtinOnly)) {
		if builtinOnly[name](overlay) {
			errs = append(errs, fmt.Errorf("%s: only the built-in catalogue sets it", name))
		}
	}
	return errs
}

// SecretWhen names a sensitive argument whose value is a secret only
// where another argument of the resource has one of some values, such as
// an SSM parameter's value, which only a SecureString's is: elsewhere,
// the generated configuration has the value as it is.
type SecretWhen struct {
	// Argument is the sensitive argument, When the argument that decides,
	// and In the values of When that make Argument a secret.
	Argument string   `yaml:"argument"`
	When     string   `yaml:"when"`
	In       []string `yaml:"in"`
}

// SecretWhen returns the type's sensitive argument that is a secret only
// where another argument says so, if it has one.
func (c *Catalogue) SecretWhen(typ string) (SecretWhen, bool) {
	e, ok := c.entries[typ]
	if !ok || e.SecretWhen == nil {
		return SecretWhen{}, false
	}
	return SecretWhen{Argument: e.SecretWhen.Argument, When: e.SecretWhen.When, In: slices.Clone(e.SecretWhen.In)}, true
}

// checkGeneration reports what's wrong with the fields of e the engine
// reads (see ForType).
func checkGeneration(typ string, e *Entry) []error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if typ == AllTypes {
		for _, name := range slices.Sorted(maps.Keys(builtinOnly)) {
			if !everyType[name] && builtinOnly[name](e) {
				fail("the entry for every type takes no %s", name)
			}
		}
	}
	if s := e.SecretWhen; s != nil && (!identifier.MatchString(s.Argument) || !identifier.MatchString(s.When) || len(s.In) == 0) {
		fail("secret_when: want an argument, the argument that decides, and the values of that one that make it secret")
	}
	if b := e.BlocksAsList; b != nil && (!identifier.MatchString(b.Block) || !identifier.MatchString(b.Attribute) || !identifier.MatchString(b.Argument)) {
		fail("blocks_as_list: want a block, its attribute and the argument that takes the list")
	}
	if p := e.Principal; p != nil {
		if !identifier.MatchString(p.Argument) || strings.TrimSpace(p.Directory) == "" || strings.TrimSpace(p.Note) == "" {
			fail("principal: want an argument, the directory and a note")
		}
		if (p.KindArgument == "") != (len(p.Kinds) == 0) || (p.KindArgument != "" && !identifier.MatchString(p.KindArgument)) {
			fail("principal: want kind_argument and kinds together")
		}
	}
	if m := e.ModuleReferences; m != nil && (!identifier.MatchString(m.Attribute) || len(m.Arguments) == 0 || slices.ContainsFunc(m.Arguments, func(a string) bool { return !identifier.MatchString(a) })) {
		fail("module_references: want an attribute and the module arguments that name the resource by it")
	}
	if d := e.DerivedIdentity; d != nil {
		if len(d.Fields()) == 0 || slices.ContainsFunc(d.parts, func(p templatePart) bool { return len(p.filters) > 0 }) {
			fail("derived_identity %q: want attributes in braces, without filters, and the text between them", d)
		}
	}
	return errs
}

// copyGeneration copies into copied, a copy of e, the fields of e the
// engine reads, sharing nothing with e.
func copyGeneration(copied, e *Entry) {
	copied.BlocksAsList, copied.Container = clonePointer(e.BlocksAsList), clonePointer(e.Container)
	if e.ModuleReferences != nil {
		copied.ModuleReferences = &ModuleReferences{Attribute: e.ModuleReferences.Attribute, Arguments: slices.Clone(e.ModuleReferences.Arguments)}
	}
	if e.Principal != nil {
		p := *e.Principal
		p.Kinds = maps.Clone(p.Kinds)
		copied.Principal = &p
	}
	if e.SecretWhen != nil {
		copied.SecretWhen = &SecretWhen{Argument: e.SecretWhen.Argument, When: e.SecretWhen.When, In: slices.Clone(e.SecretWhen.In)}
	}
}

// BlocksAsList is a type's nested block that only names an element of a
// list, which an argument of the type takes as the list instead, such as
// a load balancer's subnet_mapping blocks, each with a subnet_id, and its
// subnets: blocks that name every element of a module's list output, and
// nothing else, become the argument, set to the output.
type BlocksAsList struct {
	Block     string `yaml:"block"`
	Attribute string `yaml:"attribute"`
	Argument  string `yaml:"argument"`
}

// BlocksAsList returns the type's blocks that only name an element of a
// list, if it has them.
func (c *Catalogue) BlocksAsList(typ string) (BlocksAsList, bool) {
	e, ok := c.entries[typ]
	if !ok || e.BlocksAsList == nil {
		return BlocksAsList{}, false
	}
	return *e.BlocksAsList, true
}

// Principal is an argument of a type that names an object of the cloud's
// directory, such as an Azure role assignment's principal_id, an Entra
// ID user, group or application: where it is still a literal, no
// resource the import brought in is that object, as the directory's
// objects belong to another provider, and the engine notes it after the
// value, as "# <Directory> <kind>: <Note>".
type Principal struct {
	Argument string `yaml:"argument"`
	// KindArgument, if set, is the argument that says what kind of
	// object it is, and Kinds the kind of each of its values: user for
	// User. Without one, or a kind, it is an object.
	KindArgument string            `yaml:"kind_argument,omitempty"`
	Kinds        map[string]string `yaml:"kinds,omitempty"`
	Directory    string            `yaml:"directory"`
	Note         string            `yaml:"note"`
}

// Principal returns the type's argument that names an object of the
// cloud's directory, if it has one.
func (c *Catalogue) Principal(typ string) (Principal, bool) {
	e, ok := c.entries[typ]
	if !ok || e.Principal == nil {
		return Principal{}, false
	}
	p := *e.Principal
	p.Kinds = maps.Clone(p.Kinds)
	return p, true
}

// ModuleReferences are the arguments of module calls that name a
// resource of a type by one of its attributes, alone or in a list, such
// as the curated modules' project_id and projects, which name a Google
// project by its project_id: where the root manages the resource, the
// call refers to it, as a resource would, rather than repeat its value.
type ModuleReferences struct {
	Attribute string   `yaml:"attribute"`
	Arguments []string `yaml:"arguments"`
}

// ModuleReferences returns the module arguments that name a resource of
// the type, if any do.
func (c *Catalogue) ModuleReferences(typ string) (ModuleReferences, bool) {
	e, ok := c.entries[typ]
	if !ok || e.ModuleReferences == nil {
		return ModuleReferences{}, false
	}
	return ModuleReferences{Attribute: e.ModuleReferences.Attribute, Arguments: slices.Clone(e.ModuleReferences.Arguments)}, true
}

// Container is a type whose resources hold others, such as an Azure
// resource group or a Google project: the IDs of what is in one start
// with its ID, and read better whole than as a reference to it followed
// by the rest.
type Container struct {
	// Location says the resources in one share its location unless they
	// set their own, so that a resource in it whose location is the
	// container's refers to the container's.
	Location bool `yaml:"location,omitempty"`
}

// Container returns whether the type's resources hold others, and how,
// if they do.
func (c *Catalogue) Container(typ string) (Container, bool) {
	e, ok := c.entries[typ]
	if !ok || e.Container == nil {
		return Container{}, false
	}
	return *e.Container, true
}

// IdentityPart is a part of a derived identity: Text, or the value of
// Attribute.
type IdentityPart struct {
	Text, Attribute string
}

// DerivedIdentity returns the parts of the identity the type's resources
// have of several attributes, as Terraform reads it back in other
// resources, such as an ECS task definition's family:revision, if they
// have one.
func (c *Catalogue) DerivedIdentity(typ string) ([]IdentityPart, bool) {
	e, ok := c.entries[typ]
	if !ok || e.DerivedIdentity == nil {
		return nil, false
	}
	parts := make([]IdentityPart, len(e.DerivedIdentity.parts))
	for i, p := range e.DerivedIdentity.parts {
		parts[i] = IdentityPart{Text: p.text, Attribute: p.field}
	}
	return parts, true
}

// UnstableIDSuffix returns the text that starts the part of the import
// IDs of typ's resources that changes while the resource stays the same,
// such as the ?api-version= of an azapi resource's ID, which names the
// newest stable API version of its type when it was imported: the
// type's own, else that of the entry for every type, "" for none.
func (c *Catalogue) UnstableIDSuffix(typ string) string {
	for _, t := range []string{typ, AllTypes} {
		if e, ok := c.entries[t]; ok && e.UnstableIDSuffix != "" {
			return e.UnstableIDSuffix
		}
	}
	return ""
}
