// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// AddressType returns the type of the resource at address, in module calls
// or not, a data source's too: aws_s3_bucket for aws_s3_bucket.logs,
// module.logs["a.b"].aws_s3_bucket.this[0] and data.aws_s3_bucket.logs.
// An address of no resource, such as module.logs, a module call alone,
// has none: "".
func AddressType(address string) string {
	names := AddressNames(address)
	for len(names) > 0 && names[0] == "module" {
		if len(names) < 2 {
			return ""
		}
		names = names[2:]
	}
	if len(names) > 0 && names[0] == "data" {
		names = names[1:]
	}
	if len(names) < 2 {
		return ""
	}
	return names[0]
}

// BlockAddress returns the address of a resource block or a module call,
// or "".
func BlockAddress(b *hclwrite.Block) string {
	switch {
	case b.Type() == "resource" && len(b.Labels()) == 2:
		return b.Labels()[0] + "." + b.Labels()[1]
	case b.Type() == "module" && len(b.Labels()) == 1:
		return "module." + b.Labels()[0]
	}
	return ""
}

// MovedFrom returns the name of the root (see run.Names) that b, a moved or
// removed block, takes a resource from: type.label for a resource of the
// root, or module.name for one in a module call.
func MovedFrom(b *hclsyntax.Block) (string, bool) {
	attr, ok := b.Body.Attributes["from"]
	if !ok {
		return "", false
	}
	traversal, diags := hcl.AbsTraversalForExpr(attr.Expr)
	if diags.HasErrors() || len(traversal) < 2 {
		return "", false
	}
	next, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return "", false
	}
	if traversal.RootName() == "module" {
		return "module." + next.Name, true
	}
	return traversal.RootName() + "." + next.Name, true
}

// ImportTargets returns where each import block of f with a literal ID,
// or identity, imports into, by resource type and import ID (see
// ImportIDOf).
func ImportTargets(f *File) map[string]string {
	targets := map[string]string{}
	for _, b := range f.Syntax.Blocks {
		to, ok := b.Body.Attributes["to"]
		if b.Type != "import" || !ok {
			continue
		}
		id, ok := ImportIDOf(b.Body)
		if !ok {
			continue
		}
		address := strings.TrimSpace(string(to.Expr.Range().SliceBytes(f.Src)))
		targets[AddressType(address)+"\x00"+id] = address
	}
	return targets
}
