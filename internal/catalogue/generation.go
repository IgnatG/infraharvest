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
	"blocks_as_list": func(e *Entry) bool { return e.BlocksAsList != nil },
	"secret_when":    func(e *Entry) bool { return e.SecretWhen != nil },
}

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
	if typ == AllTypes && (e.SecretWhen != nil || e.BlocksAsList != nil) {
		fail("the entry for every type takes no secret_when or blocks_as_list")
	}
	if s := e.SecretWhen; s != nil && (!identifier.MatchString(s.Argument) || !identifier.MatchString(s.When) || len(s.In) == 0) {
		fail("secret_when: want an argument, the argument that decides, and the values of that one that make it secret")
	}
	if b := e.BlocksAsList; b != nil && (!identifier.MatchString(b.Block) || !identifier.MatchString(b.Attribute) || !identifier.MatchString(b.Argument)) {
		fail("blocks_as_list: want a block, its attribute and the argument that takes the list")
	}
	return errs
}

// copyGeneration copies into copied, a copy of e, the fields of e the
// engine reads, sharing nothing with e.
func copyGeneration(copied, e *Entry) {
	copied.BlocksAsList = clonePointer(e.BlocksAsList)
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
