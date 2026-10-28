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

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// blockFiles returns the file each block goes in, by address: each
// resource its import's file (see Import.File), and each module call the
// file of the first resource it took.
func blockFiles(imports []rootfile.Import, calls []ModuleCall) map[string]string {
	fileOf := map[string]string{}
	for _, imp := range imports {
		if imp.File != "" && strings.HasSuffix(imp.File, ".tf") && !rootfile.IsReserved(imp.File) {
			fileOf[imp.Type+"."+imp.Name] = imp.File
		}
	}
	for _, c := range calls {
		if c.Declined == "" && len(c.Resources) > 0 {
			if f := fileOf[c.Resources[0]]; f != "" {
				fileOf["module."+c.Name] = f
			}
		}
	}
	return fileOf
}

// blockAddress returns the address of a resource block or a module call,
// or "".
func blockAddress(b *hclwrite.Block) string {
	switch {
	case b.Type() == "resource" && len(b.Labels()) == 2:
		return b.Labels()[0] + "." + b.Labels()[1]
	case b.Type() == "module" && len(b.Labels()) == 1:
		return "module." + b.Labels()[0]
	}
	return ""
}

// splitFiles moves the blocks of generated.tf into the files the imports
// name (see blockFiles), such as network.tf for a VPC. Blocks that name no
// file stay in generated.tf, which goes when nothing is left in it. It
// returns the files it wrote.
func splitFiles(dir string, imports []rootfile.Import, calls []ModuleCall) ([]string, error) {
	fileOf := blockFiles(imports, calls)
	if len(fileOf) == 0 {
		return nil, nil
	}
	path := filepath.Join(dir, rootfile.GeneratedFileName)
	generated, err := hclfile.Load(path)
	if err != nil {
		return nil, err
	}
	files := map[string]*hclwrite.File{}
	blocks := generated.Edit().Body().Blocks()
	// Where every block moves, generated.tf goes whole: removing each
	// from it would search its body for every block.
	stays := slices.ContainsFunc(blocks, func(b *hclwrite.Block) bool { return fileOf[blockAddress(b)] == "" })
	for _, b := range blocks {
		name := fileOf[blockAddress(b)]
		if name == "" {
			continue
		}
		if stays {
			generated.Edit().Body().RemoveBlock(b)
		}
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
	if stays {
		return names, generated.Save()
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return names, nil
}
