// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package discovery

import (
	"context"
	"log"
	"runtime/pprof"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Service is a provider's lister as a Discoverer: it lists into
// the lister, which keeps the resources, and returns them.
func Service(name string, provider harvest.Provider, verbose bool, filters []harvest.Filter) Discoverer {
	return &service{name: name, provider: provider, verbose: verbose, filters: filters}
}

type service struct {
	name     string
	provider harvest.Provider
	verbose  bool
	filters  []harvest.Filter
}

func (s *service) Name() string {
	return s.name
}

// Discover initialises the service, applies the filters and lists with
// List, whose API calls take ctx (see harvest.ListerBase.Context). When
// List fails in part (see harvest.Partial), or ctx is done before it
// finishes, which fails its calls, it returns what was listed with the
// error.
// Its goroutine, and those the lister starts, carry the provider's name as
// a label next to the service label List gives them.
func (s *service) Discover(ctx context.Context) (resources []harvest.Resource, err error) {
	pprof.Do(ctx, pprof.Labels("provider", s.provider.Name()), func(ctx context.Context) {
		resources, err = s.discover(ctx)
	})
	return resources, err
}

func (s *service) discover(ctx context.Context) ([]harvest.Resource, error) {
	log.Printf("%s: listing %s", s.provider.Name(), s.name)
	if err := s.provider.Select(s.name, s.verbose); err != nil {
		log.Printf("%s: can't list %s: %v", s.provider.Name(), s.name, err)
		return nil, err
	}
	lister := s.provider.Selected()
	lister.SetFilters(s.filters)
	lister.SetContext(ctx)
	err := lister.List()
	if err != nil && ctx.Err() != nil {
		// Stopped by the deadline, or an interrupt: what it listed before
		// is whole, and the caller decides whether to keep it.
		err = harvest.Partial(err)
	}
	if err != nil && !harvest.IsPartial(err) {
		log.Printf("%s: listing %s failed: %v", s.provider.Name(), s.name, err)
		return nil, err
	}
	lister.Prune()
	if err != nil {
		// What it listed is kept, and err still fails the import.
		log.Printf("%s: listed %s in part: %v", s.provider.Name(), s.name, err)
		return lister.Listed(), err
	}
	log.Printf("%s: listed %s", s.provider.Name(), s.name)
	return lister.Listed(), nil
}
