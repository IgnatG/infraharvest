// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// providerName is the Terraform provider's name, google, which a provider
// type such as beta extends to google-beta.
const providerName = "google"

// globalRegion is the region whose import takes the resources that belong
// to no region, and those in a multi-region.
const globalRegion = "global"

// projectEnv names the project to import when Init isn't given one, as for
// the Google Cloud client libraries.
const projectEnv = "GOOGLE_CLOUD_PROJECT"

// regionLookupTimeout bounds the call that checks the region in Init.
const regionLookupTimeout = time.Minute

// Provider lists one Google Cloud project in one region (see Init):
// through Cloud Asset Inventory (cloud_asset), and through the APIs of
// what Cloud Asset Inventory doesn't find (iam, cloudsql, schedulerJobs,
// dns).
type Provider struct {
	harvest.ProviderBase
	// projectName is the ID of the project to list.
	projectName string
	// region is the region to list, with an empty name for global.
	region compute.Region
	// providerType is "" for the google provider, or beta for google-beta.
	providerType string
	// catalogue is the catalogue of the import (see SetCatalogue), nil
	// until it is set.
	catalogue *catalogue.Catalogue
}

// Init takes the region (args[0]: global, the default, or a region's
// name), the project (args[1], else GOOGLE_CLOUD_PROJECT) and the provider
// type (args[2]). A region other than global is looked up through the
// Compute Engine API, with ctx, so that a misspelt --regions fails rather
// than finding nothing.
func (p *Provider) Init(ctx context.Context, args []string) error {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	project := arg(1)
	if project == "" {
		project = os.Getenv(projectEnv)
	}
	if project == "" {
		return errors.New("google: the Google Cloud project to import isn't set: use --projects, or set " + projectEnv)
	}
	p.projectName = project
	p.providerType = arg(2)
	p.region = compute.Region{}
	region := arg(0)
	if region == "" || region == globalRegion {
		return nil
	}
	name, err := lookUpRegion(ctx, project, region)
	if err != nil {
		return fmt.Errorf("google: region %s of project %s: %w", region, project, err)
	}
	p.region = compute.Region{Name: name}
	return nil
}

// lookUpRegion returns the name of region as the Compute Engine API knows
// it in project, asking with ctx.
func lookUpRegion(ctx context.Context, project, region string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, regionLookupTimeout)
	defer cancel()
	svc, err := compute.NewService(ctx, clientOptions(ctx)...)
	if err != nil {
		return "", err
	}
	found, err := svc.Regions.Get(project, region).Fields("name").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return found.Name, nil
}

// Name returns google, or google-<type> for a provider type such as
// beta.
func (p *Provider) Name() string {
	if p.providerType != "" {
		return providerName + "-" + p.providerType
	}
	return providerName
}

// Directory returns google, the name of the provider's command, which its
// roots go under with --beta too.
func (*Provider) Directory() string {
	return providerName
}

// Services returns a new lister for every service: cloud_asset, and those
// that list through an API of their own what Cloud Asset Inventory
// doesn't find.
func (*Provider) Services() map[string]harvest.Lister {
	return map[string]harvest.Lister{
		cloudAssetService:   &CloudAssetLister{},
		iamService:          &IamLister{},
		cloudSQLService:     &CloudSQLLister{},
		schedulerJobService: &SchedulerJobsLister{},
		dnsService:          &DNSRecordSetsLister{},
	}
}

// Select makes the service called name the provider's current one,
// with a new lister, and passes it the region, the project and the
// catalogue.
func (p *Provider) Select(name string, verbose bool) error {
	service, ok := p.Services()[name]
	if !ok {
		return errors.New("not a service of google; \"infraharvest services google\" lists them")
	}
	p.Current = service
	service.Configure(harvest.ListerConfig{
		Service:  name,
		Provider: p.Name(),
		Verbose:  verbose,
		Settings: map[string]interface{}{
			"region":    p.region,
			"project":   p.projectName,
			"catalogue": p.catalogue,
		},
	})
	return nil
}

// ProviderBlock returns the provider block, for the project and, but
// for the global import, the region.
func (p *Provider) ProviderBlock() map[string]interface{} {
	return map[string]interface{}{
		"provider": map[string]interface{}{
			p.Name(): p.providerConfig(),
		},
	}
}

// providerConfig is the provider block's arguments. The attribution label
// is off: the provider would add it to every imported resource, which
// would then plan a change to its labels.
func (p *Provider) providerConfig() map[string]interface{} {
	config := map[string]interface{}{
		"project":                         p.projectName,
		"add_terraform_attribution_label": false,
	}
	if p.region.Name != "" {
		config["region"] = p.region.Name
	}
	return config
}

// Scope names the project and the region this import covers, global for
// the import of what belongs to no region.
func (p *Provider) Scope(context.Context) (account, region string, err error) {
	if p.region.Name == "" {
		return p.projectName, globalRegion, nil
	}
	return p.projectName, p.region.Name, nil
}

// DefaultTags describes Google's labels: the labels argument of each
// resource, which the provider's default_labels argument applies to all.
// Labels starting with goog- are Google's own.
func (*Provider) DefaultTags() (attribute, block, reservedPrefix string) {
	return "labels", "default_labels", "goog-"
}

// EffectiveTags describes Google's labels since the provider's version
// 5.0: labels holds only those of the configuration, which an import
// leaves empty, and effective_labels every label the resource has. Those
// starting with goog- are Google's own, such as goog-terraform-provisioned,
// which the provider adds unless add_terraform_attribution_label is false:
// they stay on the resource, outside the configuration.
func (*Provider) EffectiveTags() (attribute, effective, reservedPrefix string) {
	return "labels", "effective_labels", "goog-"
}

// DefaultTagsArgument says default_labels is an argument of the provider
// block, not a block.
func (*Provider) DefaultTagsArgument() bool {
	return true
}

// ConcurrentListing says the services may list at once: each has its own
// lister and API clients, and they share only the rate limiter, which
// is safe for concurrent use.
func (*Provider) ConcurrentListing() bool {
	return true
}

// ModuleProviders configures google-beta, which some curated modules
// create resources with, for the same project and region as the import.
func (p *Provider) ModuleProviders() map[string]harvest.ModuleProvider {
	return map[string]harvest.ModuleProvider{
		"google-beta": {Source: "hashicorp/google-beta", Config: p.providerConfig()},
	}
}
