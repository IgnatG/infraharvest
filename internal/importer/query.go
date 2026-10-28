// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/discovery"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/tfquery"
	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// usesQuery reports whether the import lists through Terraform's list
// resources: whether --services names the query service, which * leaves
// out, as every provider's is opt-in, and --skip doesn't.
func usesQuery(options Options) bool {
	return slices.Contains(options.Services, harvest.QueryService) && !slices.Contains(options.Skip, harvest.QueryService)
}

// checkQuery rejects --query-types without the query service, the query
// service without --query-types or with OpenTofu, which has no terraform
// query, and types that aren't provider's or that a service of
// infraharvest's lists, which it names: its lister reads what the
// generated configuration needs, such as tags for the selection, which a
// list resource doesn't return.
func checkQuery(provider harvest.Provider, options Options) error {
	if !usesQuery(options) {
		if len(options.QueryTypes) > 0 {
			return fmt.Errorf("--query-types names the types the %s service lists: add %s to --services", harvest.QueryService, harvest.QueryService)
		}
		return nil
	}
	if options.Engine == EngineTofu {
		return fmt.Errorf("the %s service lists through Terraform's list resources (terraform query), which OpenTofu doesn't have: use --engine %s", harvest.QueryService, EngineTerraform)
	}
	prefix := catalogueName(provider.Name()) + "_"
	if len(options.QueryTypes) == 0 {
		return fmt.Errorf("the %s service lists the resource types --query-types names, such as %s...: name at least one", harvest.QueryService, prefix)
	}
	listed := provider.Profile().ListedTypes
	var errs []error
	for _, typ := range options.QueryTypes {
		service := listed[typ]
		if service == "" {
			service = listed[tftypes.Canonical(typ)]
		}
		switch {
		case !strings.HasPrefix(typ, prefix):
			errs = append(errs, fmt.Errorf("%s isn't a resource type of %s", typ, provider.Name()))
		case service != "":
			errs = append(errs, fmt.Errorf("%s is listed by the %s service: use --services %s", typ, service, service))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("--query-types: %w", errors.Join(errs...))
	}
	return nil
}

// queryDiscoverer is the query service's discoverer: its lister lists
// with query, which its context carries (see harvest.WithQuery).
type queryDiscoverer struct {
	discovery.Discoverer
	query harvest.Query
}

// Discover lists with ctx carrying the query.
func (d queryDiscoverer) Discover(ctx context.Context) ([]harvest.Resource, error) {
	return d.Discoverer.Discover(harvest.WithQuery(ctx, d.query))
}

// query returns how the query service of the call c lists through
// provider, the service's own: with Terraform at tfquery.Minimum or newer,
// found or downloaded as the call's toolchain is, the credentials
// provider gives Terraform (see terraformEnv), its provider block, and
// its scope (see harvest.ProviderWithQueryScope), in a working directory
// under the output directory's CheckpointDir.
func (r *Run) query(c *callContext, provider harvest.Provider) harvest.Query {
	options := c.options
	return func(ctx context.Context) ([]harvest.Resource, error) {
		cacheDir, err := infraharvestCacheDir()
		if err != nil {
			return nil, err
		}
		binary := tfquery.Binary()
		execPath, err := r.tools.find(ctx, binary, options.TerraformPath, filepath.Join(cacheDir, binary.Name))
		if err != nil {
			return nil, err
		}
		env, err := terraformEnv(ctx, provider)
		if err != nil {
			return nil, err
		}
		req := tfquery.Request{
			ExecPath:       execPath,
			PluginCacheDir: filepath.Join(cacheDir, "plugins"),
			Env:            env,
			Parent:         filepath.Join(options.Out, CheckpointDir),
			Provider:       engineProvider(provider),
			ProviderName:   provider.Name(),
			Types:          slices.Compact(slices.Sorted(slices.Values(options.QueryTypes))),
		}
		if scoped, ok := provider.(harvest.ProviderWithQueryScope); ok {
			req.Config, req.InScope = scoped.QueryConfig(), scoped.InQueryScope
		}
		return tfquery.List(ctx, req)
	}
}

// leaveOutQueryOverlap leaves out of what the query service listed the
// resources of the types another service of the call listed too, which
// would otherwise be imported twice, by two IDs, and logs it: the other
// service's lister reads what the generated configuration needs, such as
// tags. checkQuery refuses the types the services reference names, but a
// service that lists whatever types an inventory API returns, such as
// Azure's resource_graph, may list more.
func leaveOutQueryOverlap(provider string, resources map[string][]harvest.Resource) {
	queried := resources[harvest.QueryService]
	if len(queried) == 0 {
		return
	}
	listedBy := map[string]string{}
	for _, service := range slices.Sorted(maps.Keys(resources)) {
		if service == harvest.QueryService {
			continue
		}
		for _, r := range resources[service] {
			if typ := tftypes.Canonical(r.Address.Type); listedBy[typ] == "" {
				listedBy[typ] = service
			}
		}
	}
	left := map[string]int{}
	kept := queried[:0:0]
	for _, r := range queried {
		if listedBy[tftypes.Canonical(r.Address.Type)] != "" {
			left[r.Address.Type]++
			continue
		}
		kept = append(kept, r)
	}
	for _, typ := range slices.Sorted(maps.Keys(left)) {
		log.Printf("%s: %s: leaving out the %d %s Terraform listed: the %s service lists that type", provider, harvest.QueryService, left[typ], typ, listedBy[tftypes.Canonical(typ)])
	}
	resources[harvest.QueryService] = kept
}

// engineBinaryOf is the engine binary the call with options generates
// with: Terraform at tfquery.Minimum or newer, for an import that lists
// through Terraform, which imports what it lists by identity too (see
// engineBinary).
func engineBinaryOf(options Options) engine.Binary {
	if usesQuery(options) {
		return tfquery.Binary()
	}
	return engineBinary(options.Engine)
}
