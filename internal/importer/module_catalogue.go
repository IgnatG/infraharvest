// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/config"
)

// catalogueAdapters returns the adapters of own, the organisation's
// modules (see fetchAdapters), whose resources are provider's.
func catalogueAdapters(own []adapters.Adapter, provider string) []adapters.Adapter {
	var list []adapters.Adapter
	for _, a := range own {
		if strings.HasPrefix(a.Anchor, provider+"_") {
			list = append(list, a)
		}
	}
	return list
}

// fetchModulesParallel is how many modules fetchAdapters fetches at once.
const fetchModulesParallel = 4

// fetchAdapters returns an adapter for each of the organisation's own
// modules (see adapters.Generic), in their order, fetched with the engine
// at execPath, a few at a time: git and registry modules as Terraform
// fetches them, with its credentials. A module that can't be fetched or
// matched is logged and left out.
func fetchAdapters(ctx context.Context, execPath string, modules []config.Module) []adapters.Adapter {
	fetched := make([]*adapters.Adapter, len(modules))
	slots := make(chan struct{}, fetchModulesParallel)
	var wg sync.WaitGroup
	for i, m := range modules {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			dir, cleanup, err := fetchModule(ctx, execPath, m)
			if err != nil {
				log.Printf("module %s: %v; not used", m.Source, err)
				return
			}
			a, err := adapters.Generic(m.Source, m.Version, dir)
			cleanup()
			if err != nil {
				log.Printf("module %s isn't used: imported resources can't be matched to it, as %v", m.Source, err)
				return
			}
			fetched[i] = &a
		})
	}
	wg.Wait()
	var list []adapters.Adapter
	for _, a := range fetched {
		if a != nil {
			list = append(list, *a)
		}
	}
	return list
}

// fetchModule returns the directory with m's configuration, and a function
// that removes what fetching it left. A local module is its directory.
func fetchModule(ctx context.Context, execPath string, m config.Module) (string, func(), error) {
	if config.IsLocalModule(m.Source) {
		return m.Source, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "infraharvest-module-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	f := hclwrite.NewEmptyFile()
	body := f.Body().AppendNewBlock("module", []string{"m"}).Body()
	body.SetAttributeValue("source", cty.StringVal(m.Source))
	if m.Version != "" {
		body.SetAttributeValue("version", cty.StringVal(m.Version))
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), f.Bytes(), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := tf.Get(ctx); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("fetching: %w", err)
	}
	moduleDir, err := installedModule(dir, "m")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return moduleDir, cleanup, nil
}

// installedModule returns where Terraform, in dir, installed the module
// called key, from its modules.json.
func installedModule(dir, key string) (string, error) {
	content, err := os.ReadFile(filepath.Join(dir, ".terraform", "modules", "modules.json"))
	if err != nil {
		return "", err
	}
	var manifest struct {
		Modules []struct {
			Key string `json:"Key"`
			Dir string `json:"Dir"`
		} `json:"Modules"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return "", fmt.Errorf("modules.json: %w", err)
	}
	for _, m := range manifest.Modules {
		if m.Key == key {
			return filepath.Join(dir, filepath.FromSlash(m.Dir)), nil
		}
	}
	return "", fmt.Errorf("modules.json has no module %s", key)
}
