// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/logging"
)

// inventoryVersion is the format of saved inventories. Version 1 kept the
// listers' resources as they are; version 2 keeps records.
const inventoryVersion = 2

// InventoryVersion is inventoryVersion, for a package that reads saved
// inventories without the importer, such as export, to test that it reads
// the format the importer writes.
const InventoryVersion = inventoryVersion

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
	// Filter is the --id, --tag and --where the resources were listed with (see
	// Options.filterArgs).
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
	// ID is the ID the lister found (see harvest.Observation).
	ID string `json:"id"`
	// Name is its name as listed.
	Name string `json:"name,omitempty"`
	// Tags are its tags (labels on Google Cloud), if they could be read.
	Tags map[string]string `json:"tags,omitempty"`
	// Attributes are what the lister recorded about it.
	Attributes map[string]string `json:"attributes,omitempty"`
	// Identity is the identity Terraform imports it by, for what
	// Terraform's list resources returned (see harvest.Observation).
	Identity map[string]string `json:"identity,omitempty"`
}

// listing is what a provider call listed: resources by service, and their
// tags by "type id" (see tagKey).
type listing struct {
	resources map[string][]harvest.Resource
	tags      map[string]map[string]string
	// saved is whether the resources come from what discover saved (see
	// listResources) rather than from listing them now.
	saved bool
}

// tagKey is how listing.tags and ProviderWithTags key a resource.
func tagKey(r harvest.Resource) string {
	return r.Address.Type + " " + r.Observed.ID
}

// tagsOf returns the tags of r, nil if it has none.
func (l listing) tagsOf(r harvest.Resource) map[string]string {
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
				Type:       r.Address.Type,
				ID:         r.Observed.ID,
				Name:       r.RawName,
				Tags:       l.tagsOf(r),
				Attributes: r.Observed.Attributes,
				Identity:   r.Observed.Identity,
			})
		}
	}
	slices.SortFunc(records, func(a, b record) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Type, b.Type), cmp.Compare(a.ID, b.ID), cmp.Compare(a.Name, b.Name))
	})
	return records
}

// listingOf turns records back into the resources of services, the ones
// an import asks for, from an inventory that may list more.
func listingOf(records []record, services []string) listing {
	l := listing{resources: make(map[string][]harvest.Resource, len(services)), tags: map[string]map[string]string{}}
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
		r := harvest.Resource{
			Address:  &harvest.Address{Type: rec.Type},
			Observed: &harvest.Observation{ID: rec.ID, Attributes: attributes, Identity: rec.Identity},
			RawName:  rec.Name,
			Provider: rec.Provider,
		}
		l.resources[rec.Service] = append(l.resources[rec.Service], r)
		if len(rec.Tags) > 0 {
			l.tags[tagKey(r)] = rec.Tags
		}
	}
	return l
}

// inventoryPath is where the inventory of one provider call (its region,
// profile and role, in settings) goes, in the output directory's
// checkpoints. The path is that of the settings' key (see
// harvest.ProviderSettings.InventoryKey), as it was of the positional
// strings the settings replace, so that an inventory an earlier release
// saved is found.
func inventoryPath(out, provider string, settings harvest.ProviderSettings) string {
	var key []string
	if settings != nil {
		key = settings.InventoryKey()
	}
	sum := sha256.Sum256([]byte(strings.Join(key, "\x00")))
	return filepath.Join(out, CheckpointDir, "inventory", provider+"-"+hex.EncodeToString(sum[:])[:16]+".json")
}

func saveInventory(path string, inv inventory) error {
	// The inventory holds every resource's attributes, so it is as
	// sensitive as state: its directory is private, and so is the file.
	if err := fsutil.MkdirPrivate(filepath.Dir(path)); err != nil {
		return err
	}
	content, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	return harvest.WriteSecretFile(path, content)
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

// sameFilter reports whether two sets of filters select the same
// resources, whatever their order.
func sameFilter(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// listResources lists the resources of the services of the call c, by
// service, with their tags and the services that failed. With
// --reuse-inventory it takes them from the inventory discover saved for
// the same call, if that covers them; discover saves it, with the call's
// scope (see discoveryScope).
func listResources(ctx context.Context, provider harvest.Provider, c *callContext, settings harvest.ProviderSettings) (listing, []error, error) {
	options, scope := c.options, c.scope
	path := inventoryPath(options.Out, provider.Name(), settings)
	if options.ReuseInventory && !options.discovers() {
		inv, err := loadInventory(path, c.services, options.filterArgs())
		if err != nil {
			return listing{}, nil, err
		}
		// The path names the call's arguments, not what they resolve to:
		// another region from the environment, or other credentials, is
		// another account or region.
		if inv != nil && inv.Scope != scope {
			logging.Infof("%s: the saved inventory (%s) is of %s, not %s: listing again", provider.Name(), path, inv.Scope, scope)
			inv = nil
		}
		if inv != nil {
			logging.Infof("%s: using the resources discover listed (%s)", provider.Name(), path)
			var failures []error
			for _, f := range inv.Failures {
				// Tags it missed are missing from the records too.
				if strings.HasPrefix(f, tagsFailure) {
					failures = append(failures, errors.New(f))
					continue
				}
				for _, s := range c.services {
					if strings.HasPrefix(f, "service "+s+":") {
						failures = append(failures, errors.New(f))
					}
				}
			}
			listed := listingOf(inv.Records, c.services)
			listed.saved = true
			return listed, failures, nil
		}
		logging.Infof("%s: no saved inventory lists %s with the same filters: listing them", provider.Name(), strings.Join(c.services, ","))
	}
	// Services list at the same time only where the provider says they can.
	parallel := options.ListParallel
	if !provider.Profile().ConcurrentListing {
		parallel = 1
	}
	resources, failures, err := listServices(ctx, provider, c, parallel)
	if err != nil {
		return listing{}, nil, err
	}
	listed := listing{resources: resources}
	leaveOutQueryOverlap(provider.Name(), listed.resources)
	var tagsErr error
	listed.tags, tagsErr = readTags(ctx, provider, listed.resources)
	if tagsErr != nil {
		failures = append(failures, tagsErr)
	}
	if options.discovers() {
		inv := inventory{Version: inventoryVersion, Provider: provider.Name(), Scope: scope, Services: c.services, Filter: options.filterArgs(), Records: listed.records()}
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
// their listers recorded (see harvest.AttributeTags), and those the
// provider reads (see harvest.ProviderWithTags). If the provider can't
// read them, it returns what it has with the error: the import goes on, but
// fails as partial (see checkFailures), since selection rules, owner
// detection, layouts by environment and the report's counts by tag don't
// see the tags it missed.
func readTags(ctx context.Context, provider harvest.Provider, resources map[string][]harvest.Resource) (map[string]map[string]string, error) {
	tags := map[string]map[string]string{}
	var all []harvest.Resource
	for _, rs := range resources {
		for _, r := range rs {
			all = append(all, r)
			if t := harvest.AttributeTags(r.Observed.Attributes); t != nil {
				tags[tagKey(r)] = t
			}
		}
	}
	withTags, ok := provider.(harvest.ProviderWithTags)
	if !ok || len(all) == 0 {
		return tags, nil
	}
	read, err := withTags.Tags(ctx, all)
	if err != nil {
		err = fmt.Errorf("%s couldn't read them, so selection rules, owner detection and counts by tag miss them: %w", tagsFailure, err)
	}
	for k, t := range read {
		if tags[k] == nil {
			tags[k] = map[string]string{}
		}
		maps.Copy(tags[k], t)
	}
	return tags, err
}

// tagsFailure starts the failure of a provider call whose tags couldn't
// be read; an inventory keeps it, for an import that reuses it.
const tagsFailure = "tags:"
