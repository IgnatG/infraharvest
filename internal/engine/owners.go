// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// leaveOutOwned moves out of imports.tf and generated.tf the resources whose
// tags or labels, in the tag attributes of t as Terraform generated them,
// say another tool manages them (see OwnerTags.OwnedBy), but those keep
// names by "<type> <import ID>": the listing doesn't read the tags of
// every type, such as IAM roles'.
func leaveOutOwned(dir string, keep map[string]bool, t Traits) ([]OwnedResource, error) {
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return nil, err
	}
	imports, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, imp := range imports.Imports() {
		ids[imp.To] = imp.ID
	}
	owners := map[string]Owner{}
	errs := map[string][]tfjson.Diagnostic{}
	for r := range generated.ResourceSyntax() {
		tags := map[string]string{}
		for _, name := range t.TagAttributes {
			obj, ok := hclfile.AttributeExpr(r.Syntax.Body, name).(*hclsyntax.ObjectConsExpr)
			if !ok {
				continue
			}
			for _, item := range obj.Items {
				if k := hclfile.ObjectKey(item.KeyExpr); k != "" {
					tags[k] = hclfile.StringLiteral(item.ValueExpr)
				}
			}
		}
		typ, _, _ := strings.Cut(r.Address, ".")
		if owner, ok := t.OwnerTags.OwnedBy(tags); ok && !keep[typ+" "+ids[r.Address]] {
			owners[r.Address] = owner
			errs[r.Address] = []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "managed by " + owner.Tool}}
		}
	}
	if len(owners) == 0 {
		return nil, nil
	}
	// Moved like rejected resources, but not kept: another tool owns them.
	var discard bytes.Buffer
	rejections, err := reject(dir, errs, &discard)
	if err != nil {
		return nil, err
	}
	owned := make([]OwnedResource, 0, len(rejections))
	for _, r := range rejections {
		owned = append(owned, OwnedResource{Address: r.Address, ID: r.ID, Owner: owners[r.Address]})
	}
	return owned, nil
}
