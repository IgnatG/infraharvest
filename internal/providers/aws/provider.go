// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"slices"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// providerName is the provider's name, in the generated configuration and
// in each listed resource.
const providerName = "aws"

// Provider lists one account in one region (see Init), through the
// services of serviceTable and the catalogue's Cloud Control services.
type Provider struct {
	harvest.ProviderBase
	// region is the region to list: a region's name, GlobalPseudoRegion or
	// ProfileRegion.
	region string
	// profile is the shared-config profile, "" or "default" for none.
	profile string
	// roleARN is the role to assume before listing, "" for none.
	roleARN string
}

// Init takes the region (args[0]), the profile (args[1]) and, optionally,
// a role to assume (args[2]). It contacts nothing, so it never fails: the
// credentials are resolved when the first service lists.
func (p *Provider) Init(_ context.Context, args []string) error {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	p.region = arg(0)
	p.profile = arg(1)
	p.roleARN = arg(2)
	return nil
}

// Name returns "aws".
func (*Provider) Name() string {
	return providerName
}

// ConcurrentListing says the services of a region may list at once: each
// has its own lister, and they share only the SDK configuration, which
// is safe to share.
func (*Provider) ConcurrentListing() bool {
	return true
}

// ProviderBlock returns the provider "aws" block: the region, us-east-1
// for the global services and none for ProfileRegion, where Terraform resolves
// the same default region as the listing did; and the role to assume, if
// any.
func (p Provider) ProviderBlock() map[string]interface{} {
	config := map[string]interface{}{}
	switch p.region {
	case ProfileRegion:
		// Terraform resolves the same default region as the listing.
	case GlobalPseudoRegion:
		config["region"] = GlobalHomeRegion
	default:
		config["region"] = p.region
	}
	if p.roleARN != "" {
		config["assume_role"] = map[string]interface{}{"role_arn": p.roleARN}
	}
	return map[string]interface{}{
		"provider": map[string]interface{}{
			providerName: config,
		},
	}
}

// Services returns a new lister, wrapped in a guardedLister,
// for every service: those of serviceTable, and one Cloud Control
// lister per service of the catalogue's cloud_control entries, which
// takes the place of a lister of the same name.
func (*Provider) Services() map[string]harvest.Lister {
	supported := make(map[string]harvest.Lister, len(serviceTable))
	for _, s := range serviceTable {
		supported[s.name] = &guardedLister{lister: s.newLister()}
	}
	for name, types := range cloudControlServices() {
		supported[name] = &guardedLister{lister: &CloudControlLister{types: types}}
	}
	return supported
}

// endOfSupportServices are the services of AWS services at end of support,
// WAF Classic and MediaStore, which only old estates still hold. They are
// listed only when --services names them, so that every import of * doesn't
// ask their retired APIs in every region.
var endOfSupportServices = []string{"media_store", "waf", "waf_regional"}

// OptInServices are the services * leaves out (see endOfSupportServices).
func (*Provider) OptInServices() []string {
	return slices.Clone(endOfSupportServices)
}

// Select makes the service called name the provider's current one,
// with a new lister, and passes it the provider's settings.
func (p *Provider) Select(name string, verbose bool) error {
	service, ok := p.Services()[name]
	if !ok {
		return errors.New("not a service of aws; \"infraharvest services aws\" lists them")
	}
	p.Current = service
	service.Configure(harvest.ListerConfig{
		Service:  name,
		Provider: p.Name(),
		Verbose:  verbose,
		Settings: p.serviceArgs(),
	})
	return nil
}

// serviceArgs are the arguments every service of the provider gets: the
// region, the profile and the role, which pick its SDK configuration (see
// Service.sdkConfig).
func (p *Provider) serviceArgs() map[string]interface{} {
	return map[string]interface{}{
		"region":                 p.region,
		"profile":                p.profile,
		"role_arn":               p.roleARN,
		"skip_region_validation": true,
	}
}
