// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// withChildImports adds to each service's resources the child resources
// the provider lists for them (see harvest.ProviderWithChildImports),
// and returns each child's parent, by tagKey, and the resources it
// couldn't list children for as failures. Each resource takes calls of its
// own, such as an S3 bucket's configuration probes, so up to parallel
// resources are asked at once (one below 1); the children and the
// failures keep the order of the services, by name, and of their
// resources.
func withChildImports(ctx context.Context, provider harvest.Provider, resourcesByService map[string][]harvest.Resource, parallel int) (map[string][]harvest.Resource, map[string]string, []error) {
	parents := map[string]string{}
	withChildren, ok := provider.(harvest.ProviderWithChildImports)
	if !ok {
		return resourcesByService, parents, nil
	}
	type ask struct {
		service  string
		r        harvest.Resource
		children []harvest.Resource
		err      error
	}
	var asks []ask
	for _, service := range slices.Sorted(maps.Keys(resourcesByService)) {
		for _, r := range resourcesByService[service] {
			asks = append(asks, ask{service: service, r: r})
		}
	}
	slots := make(chan struct{}, max(parallel, 1))
	var wg sync.WaitGroup
	for i := range asks {
		// What Terraform's list resources returned has no children the
		// provider looks for: they are found by an import ID it lacks.
		if asks[i].r.Observed.Identity != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			asks[i].children, asks[i].err = withChildren.ChildImports(ctx, asks[i].r)
		}()
	}
	wg.Wait()
	var failures []error
	all := make(map[string][]harvest.Resource, len(resourcesByService))
	for service, resources := range resourcesByService {
		all[service] = append([]harvest.Resource(nil), resources...)
	}
	for _, a := range asks {
		if a.err != nil {
			failures = append(failures, fmt.Errorf("%s: %s %s: %w", a.service, a.r.Address.Type, a.r.Observed.ID, a.err))
		}
		for _, c := range a.children {
			parents[tagKey(c)] = tagKey(a.r)
		}
		all[a.service] = append(all[a.service], a.children...)
	}
	return all, parents, failures
}

// childParallel is how many resources withChildImports asks for their
// children at once: as many as services list at once (--list-parallel),
// where the provider lets them, else one.
func childParallel(provider harvest.Provider, options Options) int {
	if !provider.Profile().ConcurrentListing {
		return 1
	}
	return max(options.ListParallel, 1)
}
