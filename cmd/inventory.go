// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// inventoryVersion is the format of saved inventories.
const inventoryVersion = 1

// inventory is what discover listed for one provider call: its resources
// by service, and the services that failed, so that import
// --reuse-inventory can import from it without listing again.
type inventory struct {
	Version   int                                  `json:"version"`
	Provider  string                               `json:"provider"`
	Args      []string                             `json:"args"`
	Services  []string                             `json:"services"`
	Failures  []string                             `json:"failures,omitempty"`
	Resources map[string][]terraformutils.Resource `json:"resources"`
}

// inventoryPath is where the inventory of one provider call (its region,
// profile and role, in args) goes, in the output directory's checkpoints.
func inventoryPath(out, provider string, args []string) string {
	sum := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return filepath.Join(out, CheckpointDir, "inventory", provider+"-"+hex.EncodeToString(sum[:])[:16]+".json")
}

func saveInventory(path string, inv inventory) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// loadInventory returns the saved inventory at path if it lists every one
// of services; nil if there is none, or it doesn't.
func loadInventory(path string, services []string) (*inventory, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var inv inventory
	if err := json.Unmarshal(content, &inv); err != nil {
		return nil, err
	}
	if inv.Version != inventoryVersion {
		return nil, nil
	}
	for _, s := range services {
		if !slices.Contains(inv.Services, s) {
			return nil, nil
		}
	}
	return &inv, nil
}

// inventoryFor keeps the resources of services, the ones an import asks
// for, from an inventory that may list more.
func inventoryFor(inv *inventory, services []string) map[string][]terraformutils.Resource {
	resources := make(map[string][]terraformutils.Resource, len(services))
	for _, s := range services {
		resources[s] = inv.Resources[s]
	}
	return resources
}

// listResources lists the resources of options.Resources, by service, with
// the services that failed. With --reuse-inventory it takes them from the
// inventory discover saved for the same call, if that covers them;
// discover saves it.
func listResources(ctx context.Context, provider terraformutils.ProviderGenerator, options ImportOptions, args []string) (map[string][]terraformutils.Resource, []error, error) {
	path := inventoryPath(options.PathOutput, provider.GetName(), args)
	if options.ReuseInventory && !options.Discover {
		inv, err := loadInventory(path, options.Resources)
		if err != nil {
			return nil, nil, err
		}
		if inv != nil {
			log.Printf("%s: using the resources discover listed (%s)", provider.GetName(), path)
			var failures []error
			for _, f := range inv.Failures {
				for _, s := range options.Resources {
					if strings.HasPrefix(f, "service "+s+":") {
						failures = append(failures, errors.New(f))
					}
				}
			}
			return inventoryFor(inv, options.Resources), failures, nil
		}
		log.Printf("%s: no saved inventory lists %s: listing them", provider.GetName(), strings.Join(options.Resources, ","))
	}
	mapping := terraformutils.NewProvidersMapping(provider)
	failures, err := initAllServicesResources(ctx, mapping, options, args, nil)
	if err != nil {
		return nil, nil, err
	}
	listed := mapping.GetResourcesByService()
	if options.Discover {
		inv := inventory{Version: inventoryVersion, Provider: provider.GetName(), Args: args, Services: options.Resources, Resources: listed}
		for _, f := range failures {
			inv.Failures = append(inv.Failures, f.Error())
		}
		if err := saveInventory(path, inv); err != nil {
			return nil, nil, err
		}
	}
	return listed, failures, nil
}
