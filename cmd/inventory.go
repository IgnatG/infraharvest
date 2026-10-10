// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// inventoryVersion is the format of saved inventories. Version 1 kept the
// listers' resources as they are; version 2 keeps records.
const inventoryVersion = 2

// inventory is what discover listed for one provider call: its resources,
// and the services that failed, so that import --reuse-inventory can
// import from it without listing again. The call's arguments (region,
// profile and role) name the file (see inventoryPath) and aren't kept in
// it.
type inventory struct {
	Version  int    `json:"version"`
	Provider string `json:"provider"`
	// Scope is where the call listed: provider, account and region (see
	// discoveryScope), if the provider can say.
	Scope    string   `json:"scope,omitempty"`
	Services []string `json:"services"`
	// Filter is the --filter the resources were listed with.
	Filter   []string `json:"filter,omitempty"`
	Failures []string `json:"failures,omitempty"`
	Records  []record `json:"records"`
}

// record is one listed resource, in the same form for every provider.
type record struct {
	// Service is the service that listed it.
	Service string `json:"service"`
	// Provider is the Terraform provider of its type, such as aws.
	Provider string `json:"provider"`
	Type     string `json:"type"`
	// ID is the ID the lister found (see terraformutils.InstanceState).
	ID string `json:"id"`
	// Label and Name are its name as a Terraform label and as listed.
	Label string `json:"label"`
	Name  string `json:"name,omitempty"`
	// Tags are its tags (labels on Google Cloud), if they could be read.
	Tags map[string]string `json:"tags,omitempty"`
	// Attributes are what the lister recorded about it.
	Attributes map[string]string `json:"attributes,omitempty"`
}

// listing is what a provider call listed: resources by service, and their
// tags by "type id" (see tagKey).
type listing struct {
	resources map[string][]terraformutils.Resource
	tags      map[string]map[string]string
}

// tagKey is how listing.tags and ProviderWithTags key a resource.
func tagKey(r terraformutils.Resource) string {
	return r.InstanceInfo.Type + " " + r.InstanceState.ID
}

// tagsOf returns the tags of r, nil if it has none.
func (l listing) tagsOf(r terraformutils.Resource) map[string]string {
	return l.tags[tagKey(r)]
}

// records turns a listing into records in a stable order.
func (l listing) records() []record {
	var records []record
	for service, resources := range l.resources {
		for _, r := range resources {
			records = append(records, record{
				Service:    service,
				Provider:   r.Provider,
				Type:       r.InstanceInfo.Type,
				ID:         r.InstanceState.ID,
				Label:      r.ResourceName,
				Name:       r.RawName,
				Tags:       l.tagsOf(r),
				Attributes: r.InstanceState.Attributes,
			})
		}
	}
	slices.SortFunc(records, func(a, b record) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Type, b.Type), cmp.Compare(a.ID, b.ID), cmp.Compare(a.Label, b.Label))
	})
	return records
}

// listingOf turns records back into the resources of services, the ones
// an import asks for, from an inventory that may list more.
func listingOf(records []record, services []string) listing {
	l := listing{resources: make(map[string][]terraformutils.Resource, len(services)), tags: map[string]map[string]string{}}
	for _, s := range services {
		l.resources[s] = nil
	}
	for _, rec := range records {
		if _, ok := l.resources[rec.Service]; !ok {
			continue
		}
		attributes := rec.Attributes
		if attributes == nil {
			attributes = map[string]string{}
		}
		r := terraformutils.Resource{
			InstanceInfo:  &terraformutils.InstanceInfo{Type: rec.Type, ID: rec.Type + "." + rec.Label},
			InstanceState: &terraformutils.InstanceState{ID: rec.ID, Attributes: attributes},
			ResourceName:  rec.Label,
			RawName:       rec.Name,
			Provider:      rec.Provider,
		}
		l.resources[rec.Service] = append(l.resources[rec.Service], r)
		if len(rec.Tags) > 0 {
			l.tags[tagKey(r)] = rec.Tags
		}
	}
	return l
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
	// The inventory holds every resource's attributes, so it is as
	// sensitive as state.
	return terraformutils.WriteSecretFile(path, content)
}

// loadInventory returns the saved inventory at path if it lists every one
// of services, listed with filter; nil if there is none, or it doesn't.
func loadInventory(path string, services, filter []string) (*inventory, error) {
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
	if inv.Version != inventoryVersion || !sameFilter(inv.Filter, filter) {
		return nil, nil
	}
	for _, s := range services {
		if !slices.Contains(inv.Services, s) {
			return nil, nil
		}
	}
	return &inv, nil
}

// sameFilter reports whether two --filter values select the same
// resources, whatever their order.
func sameFilter(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// listResources lists the resources of options.Resources, by service, with
// their tags and the services that failed. With --reuse-inventory it takes
// them from the inventory discover saved for the same call, if that covers
// them; discover saves it, with scope (see discoveryScope).
func listResources(ctx context.Context, provider terraformutils.ProviderGenerator, options ImportOptions, args []string, scope string) (listing, []error, error) {
	path := inventoryPath(options.PathOutput, provider.GetName(), args)
	if options.ReuseInventory && !options.Discover {
		inv, err := loadInventory(path, options.Resources, options.Filter)
		if err != nil {
			return listing{}, nil, err
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
			return listingOf(inv.Records, options.Resources), failures, nil
		}
		log.Printf("%s: no saved inventory lists %s with the same --filter: listing them", provider.GetName(), strings.Join(options.Resources, ","))
	}
	mapping := terraformutils.NewProvidersMapping(provider)
	failures, err := initAllServicesResources(ctx, mapping, options, args)
	if err != nil {
		return listing{}, nil, err
	}
	listed := listing{resources: mapping.GetResourcesByService()}
	listed.tags = readTags(ctx, provider, listed.resources)
	if options.Discover {
		inv := inventory{Version: inventoryVersion, Provider: provider.GetName(), Scope: scope, Services: options.Resources, Filter: options.Filter, Records: listed.records()}
		for _, f := range failures {
			inv.Failures = append(inv.Failures, f.Error())
		}
		if err := saveInventory(path, inv); err != nil {
			return listing{}, nil, err
		}
	}
	return listed, failures, nil
}

// readTags returns the tags of the listed resources by "type id": those
// their listers recorded (see terraformutils.AttributeTags), and those the
// provider reads (see terraformutils.ProviderWithTags). Tags only inform
// selection and the report, so a provider that can't read them is logged,
// not failed.
func readTags(ctx context.Context, provider terraformutils.ProviderGenerator, resources map[string][]terraformutils.Resource) map[string]map[string]string {
	tags := map[string]map[string]string{}
	var all []terraformutils.Resource
	for _, rs := range resources {
		for _, r := range rs {
			all = append(all, r)
			if t := terraformutils.AttributeTags(r.InstanceState.Attributes); t != nil {
				tags[tagKey(r)] = t
			}
		}
	}
	withTags, ok := provider.(terraformutils.ProviderWithTags)
	if !ok || len(all) == 0 {
		return tags
	}
	read, err := withTags.Tags(ctx, all)
	if err != nil {
		log.Printf("%s: couldn't read tags, so rules and reports by tag won't see them: %v", provider.GetName(), err)
	}
	for k, t := range read {
		if tags[k] == nil {
			tags[k] = map[string]string{}
		}
		maps.Copy(tags[k], t)
	}
	return tags
}
