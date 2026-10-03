// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// maxRepairRounds bounds the validate-and-repair loop. Most configurations
// need one round; removing an attribute can reveal another error.
const maxRepairRounds = 5

// repair makes the configuration Terraform generated into path valid where
// that is safe: it fixes what validation rejects (see removeRejected) until
// the configuration validates or nothing more can be fixed. It reports
// whether it changed the file.
func repair(ctx context.Context, tf Terraform, path string) (bool, error) {
	repaired := false
	for range maxRepairRounds {
		out, err := tf.Validate(ctx)
		if err != nil {
			return repaired, fmt.Errorf("terraform validate: %w", err)
		}
		if out.Valid {
			break
		}
		changed, err := rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
			return removeRejected(f, syntax, filepath.Base(path), out.Diagnostics)
		})
		if err != nil {
			return repaired, err
		}
		if !changed {
			break
		}
		repaired = true
	}
	return repaired, nil
}

// rewrite parses path, lets edit change it, and writes it back formatted if
// edit reports a change. edit gets the file both as an editable tree and as
// a syntax tree with source positions; their blocks are in the same order.
func rewrite(path string, edit func(f *hclwrite.File, syntax *hclsyntax.Body) bool) (bool, error) {
	f, err := loadHCL(path)
	if err != nil {
		return false, err
	}
	if !edit(f.file, f.syntax) {
		return false, nil
	}
	return true, f.save()
}

// attribute is an attribute of the generated configuration, in both trees.
type attribute struct {
	name  string
	body  *hclwrite.Body  // the body to remove it from
	block *hclsyntax.Body // the body it belongs to, to find its siblings
	rng   hcl.Range       // its source range
	zero  bool            // whether its value is 0, false, "" or empty
}

// conflictPartners finds the other arguments an argument conflicts with:
// "conflicts with availability_zone" or "only one of `subnet_mapping,subnets`
// can be specified".
var conflictPartners = regexp.MustCompile("conflicts with ([A-Za-z0-9_]+)|only one of `([A-Za-z0-9_,]+)` can be specified")

// missingArgument finds the path of a required argument a nested block
// lacks: The argument "target_failover.0.on_unhealthy" is required.
var missingArgument = regexp.MustCompile(`The argument "([A-Za-z0-9_.]+)" is required`)

// removeRejected edits what validation errors in filename point at, where
// that keeps the meaning of the configuration:
//
//   - it removes an attribute set to its zero value (0, false, "" or empty),
//     which providers treat like unset: generated configuration lists every
//     optional argument, including ones the provider then rejects, such as
//     a 0 outside an allowed range or a false that requires other arguments;
//   - it removes one of two conflicting arguments, both of which Terraform
//     generated from the same remote value: the later attribute of a pair
//     (availability_zone_id after availability_zone), or an attribute that
//     conflicts with a nested block (subnets with subnet_mapping blocks);
//   - it turns "" into null inside a rejected attribute that holds objects,
//     such as a network ACL's ingress rules, where Terraform writes "" for
//     unset strings that the provider then validates;
//   - it removes a nested block missing a required argument when all of its
//     arguments are null, which means the block is unset.
//
// It reports whether it changed anything.
func removeRejected(f *hclwrite.File, syntax *hclsyntax.Body, filename string, diags []tfjson.Diagnostic) bool {
	attrs := collectAttributes(syntax, f.Body(), nil)
	type rejection struct {
		attr    *attribute
		message string
	}
	var rejected []rejection
	var emptyBlocks []*nestedBlock
	for _, d := range diags {
		if d.Severity != tfjson.DiagnosticSeverityError || d.Range == nil || filepath.Base(d.Range.Filename) != filename {
			continue
		}
		if m := missingArgument.FindStringSubmatch(d.Detail); m != nil {
			if b := blockByPath(syntax, f.Body(), d.Range.Start.Line, m[1]); b != nil && b.isNull() {
				emptyBlocks = append(emptyBlocks, b)
			}
			continue
		}
		if a := attributeAt(attrs, d.Range.Start.Line); a != nil {
			rejected = append(rejected, rejection{a, d.Summary + ": " + d.Detail})
		}
	}
	changed := false
	for _, b := range emptyBlocks {
		if b.parent.RemoveBlock(b.write) {
			changed = true
		}
	}
	// Zero values first, so a conflict with a removed attribute is resolved.
	removed := map[*attribute]bool{}
	for _, r := range rejected {
		if r.attr.zero {
			removed[r.attr] = true
		}
	}
	for _, r := range rejected {
		if removed[r.attr] {
			continue
		}
		if conflicts(attrs, r.attr, r.message, removed) {
			removed[r.attr] = true
			continue
		}
		if strings.Contains(r.message, `""`) && emptyStringsToNull(r.attr.body, r.attr.name) {
			changed = true
		}
	}
	for a := range removed {
		a.body.RemoveAttribute(a.name)
	}
	return changed || len(removed) > 0
}

// conflicts reports whether message says a conflicts with an argument it
// should give way to: an attribute before it in the same body that stays,
// or a nested block.
func conflicts(attrs []*attribute, a *attribute, message string, removed map[*attribute]bool) bool {
	for _, m := range conflictPartners.FindAllStringSubmatch(message, -1) {
		partners := []string{m[1]}
		if m[2] != "" {
			partners = strings.Split(m[2], ",")
		}
		for _, name := range partners {
			if name == a.name {
				continue
			}
			if partner := sibling(attrs, a, name); partner != nil && !removed[partner] && partner.rng.Start.Line < a.rng.Start.Line {
				return true
			}
			for _, b := range a.block.Blocks {
				if b.Type == name {
					return true
				}
			}
		}
	}
	return false
}

// emptyStringsToNull replaces every "" in attribute name's expression with
// null.
func emptyStringsToNull(body *hclwrite.Body, name string) bool {
	attr := body.GetAttribute(name)
	if attr == nil {
		return false
	}
	tokens := attr.Expr().BuildTokens(nil)
	fixed := make(hclwrite.Tokens, 0, len(tokens))
	changed := false
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Type == hclsyntax.TokenOQuote && i+1 < len(tokens) && tokens[i+1].Type == hclsyntax.TokenCQuote {
			fixed = append(fixed, &hclwrite.Token{Type: hclsyntax.TokenIdent, Bytes: []byte("null"), SpacesBefore: tokens[i].SpacesBefore})
			i++
			changed = true
			continue
		}
		fixed = append(fixed, tokens[i])
	}
	if changed {
		body.SetAttributeRaw(name, fixed)
	}
	return changed
}

// collectAttributes lists the attributes of a body and its nested blocks.
func collectAttributes(syntax *hclsyntax.Body, body *hclwrite.Body, attrs []*attribute) []*attribute {
	for name, a := range syntax.Attributes {
		v, diags := a.Expr.Value(nil)
		attrs = append(attrs, &attribute{
			name:  name,
			body:  body,
			block: syntax,
			rng:   a.SrcRange,
			zero:  !diags.HasErrors() && isZero(v),
		})
	}
	blocks := body.Blocks()
	for i, b := range syntax.Blocks {
		if i < len(blocks) {
			attrs = collectAttributes(b.Body, blocks[i].Body(), attrs)
		}
	}
	return attrs
}

// nestedBlock is a block inside a resource, in both trees.
type nestedBlock struct {
	typ    string
	parent *hclwrite.Body // the body to remove it from
	write  *hclwrite.Block
	syntax *hclsyntax.Block
}

// blockByPath returns the nested block that holds the argument at path
// ("target_failover.0.on_unhealthy") in the top-level block spanning line.
func blockByPath(syntax *hclsyntax.Body, body *hclwrite.Body, line int, path string) *nestedBlock {
	segments := strings.Split(path, ".")
	writeBlocks := body.Blocks()
	for i, top := range syntax.Blocks {
		if i >= len(writeBlocks) || !spans(top.Range(), line) {
			continue
		}
		var found *nestedBlock
		syntaxBody, writeBody := top.Body, writeBlocks[i].Body()
		for s := 0; s < len(segments)-1; s++ {
			index := 0
			if s+1 < len(segments)-1 {
				if n, err := strconv.Atoi(segments[s+1]); err == nil {
					index = n
				}
			}
			if _, err := strconv.Atoi(segments[s]); err == nil {
				continue
			}
			found = nil
			seen := 0
			inner := writeBody.Blocks()
			for j, b := range syntaxBody.Blocks {
				if b.Type != segments[s] || j >= len(inner) {
					continue
				}
				if seen == index {
					found = &nestedBlock{typ: b.Type, parent: writeBody, write: inner[j], syntax: b}
					break
				}
				seen++
			}
			if found == nil {
				return nil
			}
			syntaxBody, writeBody = found.syntax.Body, found.write.Body()
		}
		return found
	}
	return nil
}

// isNull reports whether every argument in the block, and in the blocks it
// nests, is null.
func (b *nestedBlock) isNull() bool {
	return nullBody(b.syntax.Body)
}

func nullBody(body *hclsyntax.Body) bool {
	for _, a := range body.Attributes {
		if v, diags := a.Expr.Value(nil); diags.HasErrors() || !v.IsNull() {
			return false
		}
	}
	for _, b := range body.Blocks {
		if !nullBody(b.Body) {
			return false
		}
	}
	return true
}

// attributeAt returns the innermost attribute whose source spans line.
func attributeAt(attrs []*attribute, line int) *attribute {
	var found *attribute
	for _, a := range attrs {
		if a.rng.Start.Line <= line && line <= a.rng.End.Line &&
			(found == nil || a.rng.End.Line-a.rng.Start.Line < found.rng.End.Line-found.rng.Start.Line) {
			found = a
		}
	}
	return found
}

// sibling returns the attribute called name in the same body as a.
func sibling(attrs []*attribute, a *attribute, name string) *attribute {
	for _, s := range attrs {
		if s.block == a.block && s.name == name {
			return s
		}
	}
	return nil
}

// isZero reports whether v is 0, false, "" or empty. null is not zero here:
// Terraform writes null for values it can't show, such as sensitive ones
// ("value = null # sensitive"), and the user has to fill those in.
func isZero(v cty.Value) bool {
	switch {
	case v.IsNull(), !v.IsKnown():
		return false
	case v.Type() == cty.String:
		return v.AsString() == ""
	case v.Type() == cty.Number:
		return v.Equals(cty.Zero).True()
	case v.Type() == cty.Bool:
		return v.False()
	case v.Type().IsObjectType():
		return len(v.Type().AttributeTypes()) == 0
	case v.CanIterateElements():
		return v.LengthInt() == 0
	}
	return false
}
