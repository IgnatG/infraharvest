// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"log"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// guardedLister wraps the lister of every service of the provider. It
// passes the listing on to the lister, and adds to it what all AWS
// services share: Resource Explorer may rule the service out (see
// explorerSkip), and a service AWS doesn't offer in the region lists
// nothing there rather than failing.
type guardedLister struct {
	lister harvest.Lister
	// settings are the provider's settings, which the lister has too:
	// they say which region explorerSkip and where read.
	settings serviceSettings
	// explorerTypes are the Resource Explorer types that let the index
	// rule the service out, as the function explorerTypes finds them; none
	// without Provider.UseResourceExplorer.
	explorerTypes []string
}

// List lists the service, unless Resource Explorer shows the
// region has none of its resources, or AWS doesn't offer it there.
func (s *guardedLister) List(ctx context.Context, settings harvest.Settings) ([]harvest.Resource, error) {
	if reason := s.explorerSkip(ctx, settings.Service); reason != "" {
		log.Printf("aws: %s: %s; not listing it", settings.Service, reason)
		return nil, nil
	}
	resources, err := s.lister.List(ctx, settings)
	if err != nil && awsCodes.Classify(err) == harvest.OutcomeSkipRegion {
		log.Printf("aws: %s: not offered in %s: %v; not listing it", settings.Service, s.where(), err)
		return resources, nil
	}
	return resources, err
}

// where names the region the service lists, for a log.
func (s *guardedLister) where() string {
	region := s.settings.region
	if region == ProfileRegion {
		return "the default region"
	}
	return region
}
