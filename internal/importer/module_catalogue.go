// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/catalogue"
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

// forOtherProvider reports whether m is a registry module for a provider
// other than provider, such as google, or google-beta's google: one an
// import of provider has no use for, which it needn't fetch. A registry
// module whose source ends with a provider infraharvest imports, one with
// a built-in catalogue, is for that provider's resources only; one for
// any other provider may have provider's too.
func forOtherProvider(m config.Module, provider string) bool {
	base, _, _ := strings.Cut(provider, "-")
	p := m.RegistryProvider()
	if p == "" || p == base {
		return false
	}
	imported, err := catalogue.Providers()
	return err == nil && slices.Contains(imported, p)
}

// fetchAdapters returns an adapter for each of the organisation's own
// modules (see adapters.Generic) that can be of provider's, in their order,
// fetched with the engine at execPath, through tools (see callTools.get),
// a few at a time: git and registry modules as Terraform fetches them,
// with its credentials. Modules that
// can't change are fetched once, into cacheDir (see fetchModule). A module
// that can't be fetched or matched is logged and left out.
func fetchAdapters(ctx context.Context, tools *callTools, execPath, cacheDir, provider string, modules []config.Module) []adapters.Adapter {
	fetched := make([]*adapters.Adapter, len(modules))
	slots := make(chan struct{}, fetchModulesParallel)
	var wg sync.WaitGroup
	for i, m := range modules {
		if forOtherProvider(m, provider) {
			continue
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			dir, cleanup, err := fetchModule(ctx, tools, execPath, cacheDir, m)
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
// that removes what fetching it left. A local module is its directory. A
// module that can't change (see config.Module.Immutable) is fetched once
// into cacheDir, unless cacheDir is "", and found there after that.
func fetchModule(ctx context.Context, tools *callTools, execPath, cacheDir string, m config.Module) (string, func(), error) {
	if config.IsLocalModule(m.Source) {
		return m.Source, func() {}, nil
	}
	cached, parent := "", ""
	if cacheDir != "" && m.Immutable() {
		cached = cachedModule(cacheDir, m)
		if moduleDir, err := installedModule(cached, "m"); err == nil && isDir(moduleDir) {
			return moduleDir, func() {}, nil
		}
		// Fetched next to the cache, so that it moves there in one rename.
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return "", nil, err
		}
		parent = cacheDir
	}
	dir, err := fetchInto(ctx, tools, execPath, parent, m)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if cached != "" {
		// Into the cache whole, so that it never holds half a module; if
		// another import put it there first, that one is used.
		if err := os.Rename(dir, cached); err == nil || isDir(cached) {
			cleanup()
			if moduleDir, err := installedModule(cached, "m"); err == nil {
				return moduleDir, func() {}, nil
			}
			return "", nil, fmt.Errorf("the cached module in %s is incomplete", cached)
		}
	}
	moduleDir, err := installedModule(dir, "m")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return moduleDir, cleanup, nil
}

// cachedModule returns where in cacheDir m is kept, by its source and
// version.
func cachedModule(cacheDir string, m config.Module) string {
	sum := sha256.Sum256([]byte(m.Source + "\x00" + m.Version))
	return filepath.Join(cacheDir, hex.EncodeToString(sum[:16]))
}

// isDir reports whether path is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fetchInto fetches m with the engine at execPath into a new directory, in
// parent, or the system's temporary directory if parent is "", and returns
// that directory: a root with one module call, m, which Terraform
// installs under .terraform/modules.
func fetchInto(ctx context.Context, tools *callTools, execPath, parent string, m config.Module) (string, error) {
	dir, err := os.MkdirTemp(parent, "infraharvest-module-")
	if err != nil {
		return "", err
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
		return "", err
	}
	if err := tools.get(ctx, dir, execPath); err != nil {
		cleanup()
		return "", err
	}
	return dir, nil
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
