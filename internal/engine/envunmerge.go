// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// UnmergedBlock is a resource of a root environments share as a root of
// one of the environments would have it (see UnmergedBlocks).
type UnmergedBlock struct {
	// Address is its address in the environment's root, such as
	// aws_sqs_queue.audit: its type and its label in the merged root.
	Address string
	// File is the file of the merged root it is in, such as sqs.tf.
	File string
	// Text is its resource block, and Import its import block, of ID.
	Text, Import string
}

// unmergeFunctions are the functions the expressions of a merged root
// UnmergedBlocks puts values in for may call.
var unmergeFunctions = map[string]function.Function{
	"coalesce":   stdlib.CoalesceFunc,
	"concat":     stdlib.ConcatFunc,
	"format":     stdlib.FormatFunc,
	"join":       stdlib.JoinFunc,
	"jsonencode": stdlib.JSONEncodeFunc,
	"lookup":     stdlib.LookupFunc,
	"lower":      stdlib.LowerFunc,
	"merge":      stdlib.MergeFunc,
	"replace":    stdlib.ReplaceFunc,
	"upper":      stdlib.UpperFunc,
}

// UnmergedBlocks returns, for environment env of the root environments
// share in dir, the blocks a root of env's own would have for the
// resources at the addresses of ids, their instance addresses in the
// merged root, such as aws_sqs_queue.audit[0], with their import IDs:
// each resource block without its count, the arguments that read
// variables or locals made the values env's var files, vars/common.tfvars
// and vars/<env>/terraform.tfvars, or the variables' defaults give them,
// with an import block of its ID. Merged again with the environment's
// other resources, each comes back as the merged root has it, as for
// resources an incremental import found no longer in the cloud, which
// no listing gives back. It returns, by address in the merged root, the
// blocks, or why one can't be told, where one can't: it is in a module
// call, or a key of a for_each; the merged root has no block for it; it
// takes another provider configuration, or has a dynamic block; or an
// argument reads what the var files don't set, such as a data source.
func UnmergedBlocks(dir, env string, ids map[string]string) (map[string]UnmergedBlock, string, error) {
	files, err := configFiles(dir)
	if err != nil {
		return nil, "", err
	}
	vars := map[string]cty.Value{}
	locals := map[string]hclsyntax.Expression{}
	type found struct {
		block *hclsyntax.Block
		file  *hclFile
	}
	resources := map[string]found{}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			switch {
			case b.Type == "variable" && len(b.Labels) == 1:
				if d, ok := b.Body.Attributes["default"]; ok {
					if v, diags := d.Expr.Value(nil); !diags.HasErrors() {
						vars[b.Labels[0]] = v
					}
				}
			case b.Type == "locals":
				for name, attr := range b.Body.Attributes {
					locals[name] = attr.Expr
				}
			case b.Type == "resource" && len(b.Labels) == 2:
				resources[b.Labels[0]+"."+b.Labels[1]] = found{b, f}
			}
		}
	}
	for _, path := range []string{filepath.Join(dir, varsDirName, commonVarsFileName), filepath.Join(dir, varsDirName, env, envVarsFileName)} {
		values, err := varFileValues(path)
		if err != nil {
			return nil, "", err
		}
		maps.Copy(vars, values)
	}
	ctx := unmergeContext(vars, locals)
	blocks := map[string]UnmergedBlock{}
	for _, address := range slices.Sorted(maps.Keys(ids)) {
		if strings.HasPrefix(address, "module.") {
			return nil, address + " is in a module call", nil
		}
		traversal, diags := hclsyntax.ParseTraversalAbs([]byte(address), "", hcl.InitialPos)
		if diags.HasErrors() || len(traversal) < 2 || len(traversal) > 3 {
			return nil, address + " isn't the address of a resource", nil
		}
		label, ok := traversal[1].(hcl.TraverseAttr)
		if !ok {
			return nil, address + " isn't the address of a resource", nil
		}
		if len(traversal) == 3 {
			if index, ok := traversal[2].(hcl.TraverseIndex); !ok || index.Key.Type() != cty.Number {
				return nil, address + " is a key of a for_each", nil
			}
		}
		block := traversal.RootName() + "." + label.Name
		r, ok := resources[block]
		if !ok {
			return nil, "the merged root has no block for " + address, nil
		}
		if _, ok := r.block.Body.Attributes["for_each"]; ok {
			return nil, address + " is a key of a for_each", nil
		}
		if _, ok := r.block.Body.Attributes["provider"]; ok {
			return nil, address + " takes another provider configuration", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "resource %q %q {\n", traversal.RootName(), label.Name)
		if why := unmergedBody(&b, r.block.Body, r.file.src, ctx, true); why != "" {
			return nil, address + ": " + why, nil
		}
		b.WriteString("}\n")
		blocks[address] = UnmergedBlock{
			Address: block,
			File:    filepath.Base(r.file.path),
			Text:    string(hclwrite.Format([]byte(b.String()))),
			Import:  string(hclwrite.Format([]byte(fmt.Sprintf("import {\nto = %s\nid = %s\n}\n", block, strconv.Quote(ids[address]))))),
		}
	}
	return blocks, "", nil
}

// unmergeContext returns the evaluation context of UnmergedBlocks: vars,
// the variables' values, and those of locals that they, and the other
// locals, give, with unmergeFunctions.
func unmergeContext(vars map[string]cty.Value, locals map[string]hclsyntax.Expression) *hcl.EvalContext {
	values := map[string]cty.Value{}
	ctx := &hcl.EvalContext{Functions: unmergeFunctions}
	set := func() {
		ctx.Variables = map[string]cty.Value{"var": cty.ObjectVal(vars), "local": cty.ObjectVal(values)}
	}
	set()
	for changed := true; changed; {
		changed = false
		for _, name := range slices.Sorted(maps.Keys(locals)) {
			if _, done := values[name]; done {
				continue
			}
			v, diags := locals[name].Value(ctx)
			if diags.HasErrors() || !v.IsWhollyKnown() {
				continue
			}
			values[name] = v
			set()
			changed = true
		}
	}
	return ctx
}

// unmergedBody writes body, of a resource block of a merged root whose
// source is src, into b as UnmergedBlocks does, with the values of ctx
// put in; top says it is the resource's own body, whose count goes. It
// returns why it can't, where it can't.
func unmergedBody(b *strings.Builder, body *hclsyntax.Body, src []byte, ctx *hcl.EvalContext, top bool) string {
	attrs := slices.Collect(maps.Values(body.Attributes))
	slices.SortFunc(attrs, func(x, y *hclsyntax.Attribute) int { return x.SrcRange.Start.Byte - y.SrcRange.Start.Byte })
	for _, attr := range attrs {
		if top && attr.Name == "count" {
			continue
		}
		text := string(attr.Expr.Range().SliceBytes(src))
		reads := false
		for _, t := range attr.Expr.Variables() {
			switch t.RootName() {
			case "var", "local":
				reads = true
			case "count", "each", "self":
				return fmt.Sprintf("%s reads %s", attr.Name, t.RootName())
			}
		}
		if reads {
			v, diags := attr.Expr.Value(ctx)
			if diags.HasErrors() || !v.IsWhollyKnown() {
				return fmt.Sprintf("%s = %s, which the environment's var files don't tell", attr.Name, text)
			}
			if v.IsNull() {
				continue
			}
			text = string(hclwrite.TokensForValue(v).Bytes())
		}
		fmt.Fprintf(b, "%s = %s\n", attr.Name, text)
	}
	for _, nb := range body.Blocks {
		if nb.Type == "dynamic" {
			return "it has a dynamic block"
		}
		b.WriteString("\n" + nb.Type)
		for _, l := range nb.Labels {
			fmt.Fprintf(b, " %q", l)
		}
		b.WriteString(" {\n")
		if why := unmergedBody(b, nb.Body, src, ctx, false); why != "" {
			return why
		}
		b.WriteString("}\n")
	}
	return ""
}
