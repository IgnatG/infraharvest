// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// inventoryDir is where discover saves what it listed, in the output
// directory (see importer.CheckpointDir and importer.inventoryPath).
var inventoryDir = filepath.Join(".infraharvest", "inventory")

// inventoryVersion is the format of the saved inventories this reads
// (importer.InventoryVersion, which a test checks it against).
const inventoryVersion = 2

// listed is what export reads of a saved inventory: a projection, not the
// inventory. It names the fields export may use. The attributes each
// record holds, which are as sensitive as state, are not among them, so
// the decoder skips them and no code here can reach them.
type listed struct {
	Version  int    `json:"version"`
	Provider string `json:"provider"`
	Scope    string `json:"scope"`
	// Failures are why the listing failed in part; only whether there were
	// any is used.
	Failures []string `json:"failures"`
	Records  []struct {
		Type string            `json:"type"`
		ID   string            `json:"id"`
		Name string            `json:"name"`
		Tags map[string]string `json:"tags"`
	} `json:"records"`
}

// listedResource is one record of a saved inventory, with the scope of the
// call that listed it.
type listedResource struct {
	Scope, Type, ID, Name string
	Tags                  map[string]string
}

// readInventories reads the inventories discover saved in the output
// directory out, in the order of their file names. There may be none.
func readInventories(out string) ([]listedResource, error) {
	d, err := readDiscovery(out)
	if err != nil {
		return nil, err
	}
	return d.resources, nil
}

// discovery is what the inventories discover saved in an output directory
// say, with the bytes that name the run.
type discovery struct {
	resources []listedResource
	// files is how many inventories there are.
	files int
	// provider is the provider of the first inventory.
	provider string
	// failed is whether any listing failed in part.
	failed bool
	// content is the inventories' bytes, in the order of their file names.
	content []byte
}

// readDiscovery reads the inventories discover saved in the output
// directory out, in the order of their file names. There may be none.
func readDiscovery(out string) (*discovery, error) {
	files, err := filepath.Glob(filepath.Join(out, inventoryDir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	d := &discovery{files: len(files)}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var l listed
		if err := json.Unmarshal(content, &l); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(file), err)
		}
		if l.Version != inventoryVersion {
			return nil, fmt.Errorf("%s: inventory version %d, want %d: run discover again", filepath.Base(file), l.Version, inventoryVersion)
		}
		if d.provider == "" {
			d.provider = l.Provider
		}
		d.failed = d.failed || len(l.Failures) > 0
		d.content = append(d.content, content...)
		for _, rec := range l.Records {
			d.resources = append(d.resources, listedResource{Scope: l.Scope, Type: rec.Type, ID: rec.ID, Name: rec.Name, Tags: rec.Tags})
		}
	}
	return d, nil
}
