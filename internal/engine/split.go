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

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/run"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// splitFiles moves the blocks of generated.tf into the files the imports
// name (see run.BlockFiles), such as network.tf for a VPC. Blocks that name no
// file stay in generated.tf, which goes when nothing is left in it. It
// returns the files it wrote.
func splitFiles(dir string, imports []rootfile.Import, calls []ModuleCall) ([]string, error) {
	fileOf := run.BlockFiles(imports, calls)
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
	stays := slices.ContainsFunc(blocks, func(b *hclwrite.Block) bool { return fileOf[hclfile.BlockAddress(b)] == "" })
	var moved []*hclwrite.Block
	for _, b := range blocks {
		name := fileOf[hclfile.BlockAddress(b)]
		if name == "" {
			continue
		}
		moved = append(moved, b)
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
		generated.RemoveBlocks(moved)
		return names, generated.Save()
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return names, nil
}
