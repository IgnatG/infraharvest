// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
)

// refactorAddress is the address a moved or removed block names, as in
// module.vpc.aws_subnet.a[0]: the module calls it is in, without their
// instance keys, and the resource's type and label, both empty for a
// module call.
type refactorAddress struct {
	text       string
	modules    []string
	typ, label string
	// keyed says the address names an instance, as aws_subnet.a[0].
	keyed bool
}

// resource returns the resource or module call a names, without instance
// keys, as module.vpc.aws_subnet.a or module.vpc.
func (a refactorAddress) resource() string {
	parts := append([]string(nil), a.modules...)
	if a.typ != "" {
		parts = append(parts, a.typ+"."+a.label)
	}
	return strings.Join(parts, ".")
}

// parseRefactorAddress returns the address attr, the from or to of a moved
// or removed block in src, names; false if it names none.
func parseRefactorAddress(attr *hclsyntax.Attribute, src []byte) (refactorAddress, bool) {
	traversal, diags := hcl.AbsTraversalForExpr(attr.Expr)
	if diags.HasErrors() {
		return refactorAddress{}, false
	}
	a := refactorAddress{text: strings.TrimSpace(string(attr.Expr.Range().SliceBytes(src)))}
	var names []string
	for i, step := range traversal {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			names = append(names, s.Name)
		case hcl.TraverseAttr:
			names = append(names, s.Name)
		case hcl.TraverseIndex:
			if i == len(traversal)-1 {
				a.keyed = true
			}
		default:
			return refactorAddress{}, false
		}
	}
	for len(names) >= 2 && names[0] == "module" {
		a.modules = append(a.modules, "module."+names[1])
		names = names[2:]
	}
	switch len(names) {
	case 0:
		return a, len(a.modules) > 0
	case 2:
		a.typ, a.label = names[0], names[1]
		return a, true
	}
	return refactorAddress{}, false
}

// checkMoves checks the moved and removed blocks of the root in dir: each
// moved block moves a resource to one the root declares, or into a
// module call it makes, or to one a removed block then forgets, and keeps
// its type; no moved or removed block starts from what the root still
// declares, and a removed block names no instance; and the moves together
// move each resource once, with no cycle and no two onto one address (see
// conflictingMoves).
func checkMoves(dir string) (gate.Check, error) {
	check := gate.Check{Name: gate.CheckMoves, Passed: true}
	fail := func(format string, args ...any) {
		check.Passed = false
		check.Details = append(check.Details, fmt.Sprintf(format, args...))
	}
	files, err := configFiles(dir)
	if err != nil {
		return check, err
	}
	declared := map[string]bool{}
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			switch {
			case b.Type == "resource" && len(b.Labels) == 2:
				declared[b.Labels[0]+"."+b.Labels[1]] = true
			case b.Type == "module" && len(b.Labels) == 1:
				declared["module."+b.Labels[0]] = true
			}
		}
	}
	// declares reports whether the root declares what a names: the
	// resource, or the module call a is in or is.
	declares := func(a refactorAddress) bool {
		if len(a.modules) > 0 {
			return declared[a.modules[0]]
		}
		return declared[a.typ+"."+a.label]
	}
	// stillDeclared reports whether the root declares what a, the from of
	// a moved or removed block, names: a resource of the root, or a module
	// call. Of a resource in a module call the root still makes, only the
	// module can tell.
	stillDeclared := func(a refactorAddress) bool {
		switch {
		case len(a.modules) == 0:
			return declared[a.typ+"."+a.label]
		case len(a.modules) == 1 && a.typ == "":
			return declared[a.modules[0]]
		}
		return false
	}
	// What the removed blocks take, which a moved block may move a
	// resource to first, as one instance or one resource of a module call
	// that keeps others (see Removal.Instance and Retype.Removed).
	removed := map[string]bool{}
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if attr, ok := b.Body.Attributes["from"]; ok && b.Type == "removed" {
				if from, ok := parseRefactorAddress(attr, f.Src); ok {
					removed[from.text] = true
				}
			}
		}
	}
	var moved []Move
	for _, f := range files {
		where := func(b *hclsyntax.Block) string {
			return fmt.Sprintf("%s:%d", rel(dir, f.Path), b.DefRange().Start.Line)
		}
		for _, b := range f.Syntax.Blocks {
			if b.Type != "moved" && b.Type != "removed" {
				continue
			}
			fromAttr, ok := b.Body.Attributes["from"]
			if !ok {
				fail("%s: %s block has no from", where(b), b.Type)
				continue
			}
			from, ok := parseRefactorAddress(fromAttr, f.Src)
			if !ok {
				fail("%s: %s block's from isn't an address", where(b), b.Type)
				continue
			}
			if b.Type == "removed" {
				switch {
				case from.keyed:
					fail("%s: removed block from %s names an instance; it takes the whole resource or module call", where(b), from.text)
				case stillDeclared(from):
					fail("%s: removed block from %s, which the root still declares", where(b), from.text)
				}
				continue
			}
			toAttr, ok := b.Body.Attributes["to"]
			if !ok {
				fail("%s: moved block has no to", where(b))
				continue
			}
			to, ok := parseRefactorAddress(toAttr, f.Src)
			if !ok {
				fail("%s: moved block's to isn't an address", where(b))
				continue
			}
			if !declares(to) && (to.keyed || !removed[to.text]) {
				fail("%s: moved block to %s, which the root doesn't declare, and no removed block takes", where(b), to.text)
			}
			// Within one resource or module call, a moved block only changes
			// instance keys, as from aws_subnet.a to aws_subnet.a[0]. An
			// instance may leave a resource the root still declares, as
			// aws_subnet.a["x"] to aws_subnet.departed_a_x, if the resource
			// no longer has its key, which only the plan can tell.
			if from.resource() != to.resource() && !from.keyed && stillDeclared(from) {
				fail("%s: moved block from %s, which the root still declares", where(b), from.text)
			}
			switch {
			case from.typ == to.typ:
			case from.typ == "" || to.typ == "":
				fail("%s: moved block from %s to %s moves between a module call and a resource", where(b), from.text, to.text)
			default:
				fail("%s: moved block from %s to %s changes the resource type", where(b), from.text, to.text)
			}
			moved = append(moved, Move{From: from.text, To: to.text})
		}
	}
	if problem := conflictingMoves(moved, nil); problem != "" {
		fail("%s", problem)
	}
	return check, nil
}
