// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package discovery

import (
	"context"
	"log"
	"runtime/pprof"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Service is one service of a provider as a Discoverer: it makes the
// service's lister and lists with it, given settings, whose Service and
// Provider it sets itself.
func Service(name string, provider harvest.Provider, settings harvest.Settings) Discoverer {
	settings.Service, settings.Provider = name, provider.Name()
	return &service{name: name, provider: provider, settings: settings}
}

type service struct {
	name     string
	provider harvest.Provider
	settings harvest.Settings
}

func (s *service) Name() string {
	return s.name
}

// Discover makes the service's lister, lists with List, whose API calls
// take ctx, and applies the ID filters and drops duplicates (see
// harvest.Prune). When List fails in part (see harvest.Partial), or ctx
// is done before it finishes, which fails its calls, it returns what was
// listed with the error.
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
	lister, err := s.provider.Lister(s.name)
	if err != nil {
		log.Printf("%s: can't list %s: %v", s.provider.Name(), s.name, err)
		return nil, err
	}
	listed, err := lister.List(ctx, s.settings)
	if err != nil && ctx.Err() != nil {
		// Stopped by the deadline, or an interrupt: what it listed before
		// is whole, and the caller decides whether to keep it.
		err = harvest.Partial(err)
	}
	if err != nil && !harvest.IsPartial(err) {
		log.Printf("%s: listing %s failed: %v", s.provider.Name(), s.name, err)
		return nil, err
	}
	resources := harvest.Prune(listed, s.settings.Filters)
	if err != nil {
		// What it listed is kept, and err still fails the import.
		log.Printf("%s: listed %s in part: %v", s.provider.Name(), s.name, err)
		return resources, err
	}
	log.Printf("%s: listed %s", s.provider.Name(), s.name)
	return resources, nil
}
