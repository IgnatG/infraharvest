// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// BlockFiles returns the file each block goes in, by address: each
// resource its import's file (see Import.File), and each module call the
// file of the first resource it took.
func BlockFiles(imports []rootfile.Import, calls []ModuleCall) map[string]string {
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
