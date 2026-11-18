// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/run"
)

// writeCalls replaces the trials' resources in generated.tf with their
// module calls, makes references to them use the modules' outputs,
// imports into the modules, and configures the other providers the
// modules use, of moduleProviders, with the root's tags of tags.
func writeCalls(ws *run.Workspace, trials []*trial, moduleProviders map[string]rootfile.Provider, tags *DefaultTags) error {
	generated, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		return err
	}
	moved := map[string]bool{}
	importTo := map[string]string{}
	var renames []referenceRename
	for _, t := range trials {
		for _, m := range t.members() {
			moved[m] = true
			importTo[m] = t.moduleAddress(m)
			typ, name, _ := strings.Cut(m, ".")
			for attr, output := range t.call.Outputs[m] {
				replacement, err := outputTokens(t.name, output)
				if err != nil {
					return fmt.Errorf("%s: output for %s.%s: %w", t.adapter.Source, m, attr, err)
				}
				renames = append(renames, referenceRename{[]string{typ, name, attr}, replacement})
			}
		}
		for reference, v := range t.literals {
			renames = append(renames, referenceRename{strings.Split(reference, "."), hclwrite.TokensForValue(cty.StringVal(v))})
		}
	}
	byStart := indexRenames(len(renames), func(i int) []string { return renames[i].search })
	var gone []*hclwrite.Block
	for _, r := range generated.Resources() {
		if moved[r.Address] {
			gone = append(gone, r.Write)
			continue
		}
		replaceAllReferences(r.Write.Body(), renames, byStart)
	}
	// The last edit of the blocks there are (see RemoveBlocks).
	generated.RemoveBlocks(gone)
	for _, t := range trials {
		block := hclwrite.NewBlock("module", []string{t.name})
		source, err := hclfile.CallSource(ws.Dir, t.adapter.Source)
		if err != nil {
			return err
		}
		block.Body().SetAttributeValue("source", cty.StringVal(source))
		// Git and local modules have none: a git source pins its ref.
		if t.adapter.Version != "" {
			block.Body().SetAttributeValue("version", cty.StringVal(t.adapter.Version))
		}
		if t.provider != nil {
			local := strings.TrimSpace(string(t.provider.Bytes()))
			local, _, _ = strings.Cut(local, ".")
			block.Body().SetAttributeRaw("providers", hclwrite.TokensForObject([]hclwrite.ObjectAttrTokens{{Name: hclwrite.TokensForIdentifier(local), Value: t.provider}}))
		}
		block.Body().AppendNewline()
		for _, arg := range t.call.Arguments {
			// As a person would call the module: without its defaults.
			if t.adapter.IsDefault(arg.Name, arg.Value) {
				continue
			}
			block.Body().SetAttributeRaw(arg.Name, arg.Value)
		}
		replaceAllReferences(block.Body(), renames, byStart)
		generated.AppendBlocks(block)
	}
	ws.Save(generated)
	if err := writeModuleProviders(ws, trials, moduleProviders, tags); err != nil {
		return err
	}
	var added []moduleImport
	for _, t := range trials {
		for _, imp := range t.call.Imports {
			added = append(added, moduleImport{to: "module." + t.name + "." + imp.To, Import: imp})
		}
	}
	return retargetImports(ws, importTo, added)
}

// moduleImport is an import a module call adds, into to.
type moduleImport struct {
	adapters.Import
	to string
}

// retargetImports points the import blocks into the given resources at
// their new addresses, such as module.logs.aws_s3_bucket.this[0], and adds
// the imports of added: by IDs that start with the import ID of the
// resource each names, or, where that resource is imported by its
// identity, by the identity the adapter makes of its (see
// adapters.Import.Identity).
func retargetImports(ws *run.Workspace, to map[string]string, added []moduleImport) error {
	imports, err := ws.Load(rootfile.ImportsFileName)
	if err != nil {
		return err
	}
	ids := map[string]string{}
	identities := map[string]map[string]string{}
	for _, imp := range imports.Imports() {
		ids[imp.To] = imp.ID
		if _, hasID := imp.Syntax.Body.Attributes["id"]; !hasID {
			identities[imp.To], _ = hclfile.IdentityOf(imp.Syntax.Body)
		}
		address, ok := to[imp.To]
		if !ok {
			continue
		}
		traversal, diags := hclsyntax.ParseTraversalAbs([]byte(address), "", hcl.InitialPos)
		if diags.HasErrors() {
			return fmt.Errorf("import into %s: %w", address, diags)
		}
		imp.Write.Body().SetAttributeTraversal("to", traversal)
	}
	body := imports.Edit().Body()
	for _, imp := range added {
		id := ids[imp.Of]
		if id == "" {
			return fmt.Errorf("import into %s: %s has no import ID", imp.to, imp.Of)
		}
		traversal, diags := hclsyntax.ParseTraversalAbs([]byte(imp.to), "", hcl.InitialPos)
		if diags.HasErrors() {
			return fmt.Errorf("import into %s: %w", imp.to, diags)
		}
		body.AppendNewline()
		block := body.AppendNewBlock("import", nil).Body()
		block.SetAttributeTraversal("to", traversal)
		of, byIdentity := identities[imp.Of]
		if !byIdentity {
			block.SetAttributeValue("id", cty.StringVal(id+imp.Suffix))
			continue
		}
		identity := identityFrom(imp.Identity, of)
		if len(identity) == 0 {
			return fmt.Errorf("import into %s: %s is imported by its identity, which the module's adapter makes no identity of", imp.to, imp.Of)
		}
		block.SetAttributeValue("identity", rootfile.IdentityValue(identity))
	}
	ws.Save(imports)
	return nil
}

// identityFrom returns the identity spec makes of of, the identity of
// the resource an adapter's import names (see adapters.Import.Identity):
// each attribute of spec whose value is {name} is of's attribute name,
// left out where of has none, and any other value is as it is.
func identityFrom(spec, of map[string]string) map[string]string {
	identity := map[string]string{}
	for k, v := range spec {
		name, isRef := strings.CutPrefix(v, "{")
		if name, ok := strings.CutSuffix(name, "}"); isRef && ok {
			if value, ok := of[name]; ok {
				identity[k] = value
			}
			continue
		}
		identity[k] = v
	}
	return identity
}

// importTargets returns where each import block of dir imports into, by
// resource type and import ID: resources of different types can share an
// ID, such as a bucket and its versioning.
func importTargets(dir string) (map[string]string, error) {
	imports, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
	if err != nil {
		return nil, err
	}
	return hclfile.ImportTargets(imports), nil
}

// movedAddresses returns the new address of each resource whose import
// block now imports elsewhere than in before (see importTargets), by its
// old address.
func movedAddresses(dir string, before map[string]string) (map[string]string, error) {
	after, err := importTargets(dir)
	if err != nil {
		return nil, err
	}
	moved := map[string]string{}
	for key, from := range before {
		if to, ok := after[key]; ok && to != from {
			moved[from] = to
		}
	}
	return moved, nil
}

// moduleCalls lists the module calls in generated.tf, with the resources
// moved into each, followed by the clusters declined.
func moduleCalls(dir string, moved map[string]string, declined []ModuleCall) ([]ModuleCall, error) {
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return nil, err
	}
	var calls []ModuleCall
	for _, b := range generated.Syntax.Blocks {
		if b.Type != "module" || len(b.Labels) != 1 {
			continue
		}
		call := ModuleCall{Name: b.Labels[0], Source: hclfile.StringAttribute(b.Body, "source"), Version: hclfile.StringAttribute(b.Body, "version")}
		for from, to := range moved {
			if strings.HasPrefix(to, "module."+call.Name+".") {
				call.Resources = append(call.Resources, from)
			}
		}
		sort.Strings(call.Resources)
		calls = append(calls, call)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].Name < calls[j].Name })
	return append(calls, declined...), nil
}

// outputTokens returns the reference to a module output: output is an
// output's name, or a path into it, such as subnets["eu/app"].self_link
// (see adapters.Call.Outputs).
func outputTokens(call, output string) (hclwrite.Tokens, error) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("module."+call+"."+output), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	return hclwrite.TokensForTraversal(traversal), nil
}
