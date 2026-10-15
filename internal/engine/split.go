// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// reservedFiles are the files of a root that hold something else than its
// resources, which no Import.File may name.
var reservedFiles = []string{
	GeneratedFileName, ImportsFileName, VersionsFileName, ProvidersFileName, VariablesFileName,
	LocalsFileName, DataFileName, "backend.tf", "outputs.tf", "main.tf",
}

// splitFiles moves the blocks of generated.tf into the files the imports
// name (see Import.File), such as network.tf for a VPC: each resource into
// its import's file, and each module call into the file of the first
// resource it took. Blocks that name no file stay in generated.tf, which
// goes when nothing is left in it. It returns the files it wrote.
func splitFiles(dir string, imports []Import, calls []ModuleCall) ([]string, error) {
	fileOf := map[string]string{}
	for _, imp := range imports {
		if imp.File != "" && strings.HasSuffix(imp.File, ".tf") && !slices.Contains(reservedFiles, imp.File) {
			fileOf[imp.Type+"."+imp.Name] = imp.File
		}
	}
	if len(fileOf) == 0 {
		return nil, nil
	}
	for _, c := range calls {
		if c.Declined == "" && len(c.Resources) > 0 {
			if f := fileOf[c.Resources[0]]; f != "" {
				fileOf["module."+c.Name] = f
			}
		}
	}
	path := filepath.Join(dir, GeneratedFileName)
	generated, err := loadHCL(path)
	if err != nil {
		return nil, err
	}
	files := map[string]*hclwrite.File{}
	for _, b := range generated.file.Body().Blocks() {
		var address string
		switch {
		case b.Type() == "resource" && len(b.Labels()) == 2:
			address = b.Labels()[0] + "." + b.Labels()[1]
		case b.Type() == "module" && len(b.Labels()) == 1:
			address = "module." + b.Labels()[0]
		}
		name := fileOf[address]
		if name == "" {
			continue
		}
		generated.file.Body().RemoveBlock(b)
		out, ok := files[name]
		if !ok {
			out = hclwrite.NewEmptyFile()
			files[name] = out
		} else {
			out.Body().AppendNewline()
		}
		out.Body().AppendBlock(b)
	}
	names := make([]string, 0, len(files))
	for name, out := range files {
		if err := fsutil.WriteFile(filepath.Join(dir, name), bytes.TrimLeft(hclwrite.Format(out.Bytes()), "\n"), 0o644); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	slices.Sort(names)
	if len(generated.file.Body().Blocks()) > 0 {
		return names, generated.save()
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return names, nil
}
