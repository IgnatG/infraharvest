// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
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
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return nil, err
	}
	resources := map[string]*hclwrite.Block{}
	for _, r := range generated.resources() {
		resources[r.address] = r.write
	}
	importBlocks := map[string]*hclwrite.Block{}
	ids := map[string]string{}
	for _, imp := range imports.imports() {
		importBlocks[imp.to] = imp.write
		ids[imp.to] = imp.id
	}

	addresses := make([]string, 0, len(errs))
	for addr := range errs {
		addresses = append(addresses, addr)
	}
	sort.Strings(addresses)
	rejections := make([]Rejection, 0, len(addresses))
	for _, addr := range addresses {
		var messages []string
		for _, d := range errs[addr] {
			if m := diagnosticMessage(d); !slices.Contains(messages, m) {
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
		for _, moved := range []struct {
			file  *hclFile
			block *hclwrite.Block
		}{{imports, importBlocks[addr]}, {generated, resources[addr]}} {
			if moved.block == nil {
				continue
			}
			out.WriteString("\n")
			out.Write(bytes.TrimSpace(hclwrite.Format(moved.block.BuildTokens(nil).Bytes())))
			out.WriteString("\n")
			moved.file.file.Body().RemoveBlock(moved.block)
		}
	}
	if err := imports.save(); err != nil {
		return nil, err
	}
	return rejections, generated.save()
}

// diagnosticMessage renders d as "summary: detail" on one line.
func diagnosticMessage(d tfjson.Diagnostic) string {
	if detail := strings.Join(strings.Fields(d.Detail), " "); detail != "" {
		return d.Summary + ": " + detail
	}
	return d.Summary
}
