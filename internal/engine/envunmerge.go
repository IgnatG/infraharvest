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

// UnmergeEnv is the environment UnmergedBlocks gives blocks for.
type UnmergeEnv struct {
	// Name is the environment's name.
	Name string
	// Data has, by data source type, the values of the attributes, by
	// name, of the data sources that read the environment's account,
	// subscription or project and its region, as its credentials give
	// them (see MergeProfile.EnvironmentData), such as AWS's
	// aws_caller_identity's account_id; an attribute that isn't known
	// isn't there.
	Data map[string]map[string]string
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
// with an import block of its ID. Arguments that read the data sources
// of the environment's own account and region (see unmergeData), as the
// merged root's locals of them do, get the values env gives them, as
// the environment's own root has them: merged again, they become those
// locals again. Merged again with the environment's other resources,
// each comes back as the merged root has it, as for resources an
// incremental import found no longer in the cloud, which no listing
// gives back. It returns, by address in the merged root, the blocks, or
// why one can't be told, where one can't: it is in a module call, or a
// key of a for_each; the merged root has no block for it; it takes
// another provider configuration, or has a dynamic block; or an argument
// reads what the var files don't set, such as a data source with
// arguments of its own, or what env doesn't give.
func UnmergedBlocks(dir string, env UnmergeEnv, ids map[string]string) (map[string]UnmergedBlock, string, error) {
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
	var data []*hclsyntax.Block
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
			case b.Type == "data" && len(b.Labels) == 2:
				data = append(data, b)
			}
		}
	}
	values, err := mergedValues(dir, env.Name)
	if err != nil {
		return nil, "", err
	}
	maps.Copy(vars, values)
	ctx := unmergeContext(vars, locals, unmergeData(data, env))
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
		if why := unmergedBody(&b, r.block.Body, r.file.src, ctx, locals, true); why != "" {
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

// unmergeData returns, as the value of data in an expression, the data
// sources of blocks, a merged root's, whose values UnmergedBlocks tells
// for env: those that read the account, subscription or project and the
// region the environment plans in (see unmergedAttributes), with no
// arguments of their own but a count, and so no provider configuration
// of another region; a counted one is its one instance, as the resource
// that reads it, which env has, says the environment counts it. Another
// data source, as one that reads a resource by its arguments, could
// read another value for each environment, which the run can't tell.
func unmergeData(blocks []*hclsyntax.Block, env UnmergeEnv) cty.Value {
	byType := map[string]map[string]cty.Value{}
	for _, b := range blocks {
		if len(b.Body.Blocks) > 0 || slices.ContainsFunc(slices.Collect(maps.Keys(b.Body.Attributes)), func(name string) bool { return name != "count" }) {
			continue
		}
		attributes := unmergedAttributes(b.Labels[0], env)
		if len(attributes) == 0 {
			continue
		}
		v := cty.ObjectVal(attributes)
		if _, counted := b.Body.Attributes["count"]; counted {
			v = cty.TupleVal([]cty.Value{v})
		}
		if byType[b.Labels[0]] == nil {
			byType[b.Labels[0]] = map[string]cty.Value{}
		}
		byType[b.Labels[0]][b.Labels[1]] = v
	}
	data := map[string]cty.Value{}
	for typ, byName := range byType {
		data[typ] = cty.ObjectVal(byName)
	}
	return cty.ObjectVal(data)
}

// unmergedAttributes returns the attributes of a data source of type typ
// that env gives (see UnmergeEnv.Data), by name, such as the ID of its
// account, subscription or project, its AWS region, and the AWS
// partition and DNS suffix the region is in. A plan of the merged root
// as env reads the same, with env's credentials.
func unmergedAttributes(typ string, env UnmergeEnv) map[string]cty.Value {
	attributes := map[string]cty.Value{}
	for name, value := range env.Data[typ] {
		attributes[name] = cty.StringVal(value)
	}
	return attributes
}

// unmergeContext returns the evaluation context of UnmergedBlocks: vars,
// the variables' values, data, the data sources' (see unmergeData), and
// the values of locals that they, and the other locals, give, with
// unmergeFunctions.
func unmergeContext(vars map[string]cty.Value, locals map[string]hclsyntax.Expression, data cty.Value) *hcl.EvalContext {
	values := map[string]cty.Value{}
	ctx := &hcl.EvalContext{Functions: unmergeFunctions}
	set := func() {
		ctx.Variables = map[string]cty.Value{"var": cty.ObjectVal(vars), "local": cty.ObjectVal(values), "data": data}
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
// put in where an argument reads a variable, a local or a data source
// ctx gives; top says it is the resource's own body, whose count goes.
// It returns why it can't, where it can't, naming the data sources of
// locals, the merged root's, an argument reads that ctx doesn't give.
func unmergedBody(b *strings.Builder, body *hclsyntax.Body, src []byte, ctx *hcl.EvalContext, locals map[string]hclsyntax.Expression, top bool) string {
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
			case "data":
				// One unmergeData tells: the environment's own root
				// has its value, as it was generated.
				if _, diags := t.TraverseAbs(ctx); !diags.HasErrors() {
					reads = true
				}
			case "count", "each", "self":
				return fmt.Sprintf("%s reads %s", attr.Name, t.RootName())
			}
		}
		if reads {
			v, diags := attr.Expr.Value(ctx)
			if diags.HasErrors() || !v.IsWhollyKnown() {
				if untold := untoldData(attr.Expr, locals, ctx); len(untold) > 0 {
					return fmt.Sprintf("%s = %s, which reads %s, whose value in the environment the run can't tell", attr.Name, text, strings.Join(untold, ", "))
				}
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
		if why := unmergedBody(b, nb.Body, src, ctx, locals, false); why != "" {
			return why
		}
		b.WriteString("}\n")
	}
	return ""
}

// untoldData returns what expr reads of data sources, itself or through
// the locals it reads, that ctx doesn't give, such as
// data.aws_vpc.shop.id, sorted.
func untoldData(expr hclsyntax.Expression, locals map[string]hclsyntax.Expression, ctx *hcl.EvalContext) []string {
	var untold []string
	seen := map[string]bool{}
	var walk func(e hclsyntax.Expression)
	walk = func(e hclsyntax.Expression) {
		for _, t := range e.Variables() {
			switch t.RootName() {
			case "data":
				if _, diags := t.TraverseAbs(ctx); diags.HasErrors() {
					untold = append(untold, addressText(t))
				}
			case "local":
				if len(t) < 2 {
					continue
				}
				name, ok := t[1].(hcl.TraverseAttr)
				if !ok || seen[name.Name] {
					continue
				}
				seen[name.Name] = true
				if l, ok := locals[name.Name]; ok {
					walk(l)
				}
			}
		}
	}
	walk(expr)
	slices.Sort(untold)
	return slices.Compact(untold)
}
