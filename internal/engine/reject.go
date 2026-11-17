// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// Rejection is a resource Generate left out because Terraform couldn't
// import it or generate valid configuration for it. Its import block and
// any generated configuration are in rejected.hcl, under its errors.
type Rejection struct {
	Address string
	ID      string // the import ID
	Errors  []string
}

// reject moves the resources errs names out of imports.tf and generated.tf,
// and appends them, with their errors, to out.
func reject(dir string, errs map[string][]tfjson.Diagnostic, out *bytes.Buffer) ([]Rejection, error) {
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return nil, err
	}
	imports, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
	if err != nil {
		return nil, err
	}
	resources := map[string]*hclwrite.Block{}
	for _, r := range generated.Resources() {
		resources[r.Address] = r.Write
	}
	importBlocks := map[string]*hclwrite.Block{}
	ids := map[string]string{}
	for _, imp := range imports.Imports() {
		importBlocks[imp.To] = imp.Write
		ids[imp.To] = imp.ID
	}

	addresses := make([]string, 0, len(errs))
	for addr := range errs {
		addresses = append(addresses, addr)
	}
	sort.Strings(addresses)
	rejections := make([]Rejection, 0, len(addresses))
	// The blocks that go, by file, removed at once at the end.
	gone := map[*hclfile.File][]*hclwrite.Block{}
	for _, addr := range addresses {
		var messages []string
		for _, d := range errs[addr] {
			if m := tfrun.DiagnosticMessage(d); !slices.Contains(messages, m) {
				messages = append(messages, m)
			}
		}
		rejections = append(rejections, Rejection{Address: addr, ID: ids[addr], Errors: messages})

		if out.Len() > 0 {
			out.WriteString("\n")
		}
		fmt.Fprintf(out, "# %s was left out:\n", addr)
		for _, m := range messages {
			fmt.Fprintf(out, "#   %s\n", m)
		}
		// variables.tf declares only the variables of the resources kept.
		if vars := variablesIn(resources[addr]); len(vars) > 0 {
			fmt.Fprintf(out, "# Before moving it back, declare the sensitive variables it reads in %s: %s.\n", hclfile.VariablesFileName, strings.Join(vars, ", "))
		}
		for _, moved := range []struct {
			file  *hclfile.File
			block *hclwrite.Block
		}{{imports, importBlocks[addr]}, {generated, resources[addr]}} {
			if moved.block == nil {
				continue
			}
			out.WriteString("\n")
			out.Write(bytes.TrimSpace(hclwrite.Format(moved.block.BuildTokens(nil).Bytes())))
			out.WriteString("\n")
			gone[moved.file] = append(gone[moved.file], moved.block)
		}
	}
	for _, f := range []*hclfile.File{imports, generated} {
		f.RemoveBlocks(gone[f])
	}
	if err := imports.Save(); err != nil {
		return nil, err
	}
	return rejections, generated.Save()
}

// unconfigured returns, as errors by address, the import blocks in dir
// that import into a resource generated.tf has no block for.
func unconfigured(dir string) (map[string][]tfjson.Diagnostic, error) {
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return nil, err
	}
	imports, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
	if err != nil {
		return nil, err
	}
	configured := map[string]bool{}
	for r := range generated.ResourceSyntax() {
		configured[r.Address] = true
	}
	missing := map[string][]tfjson.Diagnostic{}
	for _, imp := range imports.Imports() {
		if !configured[imp.To] {
			missing[imp.To] = []tfjson.Diagnostic{{
				Severity: tfjson.DiagnosticSeverityError,
				Summary:  "Terraform generated no configuration",
				Detail:   "its import found no such resource: it may have been deleted, or be being deleted",
			}}
		}
	}
	return missing, nil
}

// variablesIn returns the names of the input variables block refers to,
// in order and once each, or none if block is nil.
func variablesIn(block *hclwrite.Block) []string {
	if block == nil {
		return nil
	}
	tokens := block.BuildTokens(nil)
	var names []string
	for i := 0; i+2 < len(tokens); i++ {
		if tokens[i].Type != hclsyntax.TokenIdent || string(tokens[i].Bytes) != "var" ||
			tokens[i+1].Type != hclsyntax.TokenDot || tokens[i+2].Type != hclsyntax.TokenIdent {
			continue
		}
		if i > 0 && tokens[i-1].Type == hclsyntax.TokenDot {
			continue // an attribute named var, as in local.var.x
		}
		if name := string(tokens[i+2].Bytes); !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// Gone is a resource Generate left out because its import found no object:
// the listing had it, and it has been deleted since, or never finished
// being created.
type Gone struct {
	Address string
	ID      string // the import ID
}

// missingObject is what Terraform and OpenTofu both say of an import ID
// whose object doesn't exist.
const missingObject = "Cannot import non-existent remote object"

// dropGone moves out of imports.tf and generated.tf the resources diags
// say don't exist, as their import found no object, and returns them with
// the diagnostics left: those about other resources, and those that name
// none, which stay as they are. Nothing is kept of the resources: there
// is nothing to fix. Terraform generates nothing for them, so their other
// errors, such as the missing configuration of their import blocks, go
// too.
func dropGone(dir string, diags []tfjson.Diagnostic) ([]Gone, []tfjson.Diagnostic, error) {
	if !slices.ContainsFunc(diags, isMissingObject) {
		return nil, diags, nil
	}
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return nil, nil, err
	}
	imports, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
	if err != nil {
		return nil, nil, err
	}
	assigned, rest := byResource(diags, generated, imports)
	errs := map[string][]tfjson.Diagnostic{}
	for _, addr := range slices.Sorted(maps.Keys(assigned)) {
		ds := assigned[addr]
		if slices.ContainsFunc(ds, isMissingObject) {
			errs[addr] = ds
			continue
		}
		rest = append(rest, ds...)
	}
	if len(errs) == 0 {
		return nil, diags, nil
	}
	var discard bytes.Buffer
	rejections, err := reject(dir, errs, &discard)
	if err != nil {
		return nil, nil, err
	}
	gone := make([]Gone, 0, len(rejections))
	for _, r := range rejections {
		gone = append(gone, Gone{Address: r.Address, ID: r.ID})
	}
	return gone, rest, nil
}

// isMissingObject reports whether d says an import found no object.
func isMissingObject(d tfjson.Diagnostic) bool {
	return d.Severity == tfjson.DiagnosticSeverityError && strings.Contains(d.Summary, missingObject)
}
