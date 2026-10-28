// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// InstalledModule is a module call Terraform installed for a root: its
// key, such as vpc or vpc.nat for a module that one calls, and the
// directory it is in.
type InstalledModule struct {
	Key, Dir string
}

// InstalledModules returns the modules Terraform installed for the root in
// dir, as its modules.json lists them; none if it installed none. Its init
// kept its working files in dataDir, its TF_DATA_DIR, or in .terraform in
// dir for "". Terraform lists the modules it downloaded into a dataDir
// named by an absolute path by their absolute paths, and the others
// relative to dir.
func InstalledModules(dir, dataDir string) ([]InstalledModule, error) {
	if dataDir == "" {
		dataDir = filepath.Join(dir, ".terraform")
	}
	content, err := os.ReadFile(filepath.Join(dataDir, "modules", "modules.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Modules []struct {
			Key string `json:"Key"`
			Dir string `json:"Dir"`
		} `json:"Modules"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return nil, fmt.Errorf("modules.json: %w", err)
	}
	var modules []InstalledModule
	for _, m := range manifest.Modules {
		if m.Key == "" || m.Dir == "" {
			continue
		}
		path := filepath.FromSlash(m.Dir)
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		modules = append(modules, InstalledModule{Key: m.Key, Dir: path})
	}
	return modules, nil
}
