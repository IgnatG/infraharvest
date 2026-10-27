// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The capabilities Provider has, checked when it builds: one whose method
// no longer matches its interface would otherwise be dropped in silence,
// as the importer asserts them at run time.
var (
	_ harvest.ProviderWithModuleProviders = (*Provider)(nil)
	_ harvest.ProviderWithPartialTypes    = (*Provider)(nil)
	_ harvest.ProviderWithScope           = (*Provider)(nil)
	_ harvest.ProviderWithCatalogue       = (*Provider)(nil)
	_ harvest.ProviderWithUnwantedTypes   = (*Provider)(nil)
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
	// unwanted reports the types the import leaves out whatever is listed
	// (see SetUnwantedTypes); nil wants every type.
	unwanted func(typ string) bool
	// caches are those of the run Init was given (see cachesOf), which
	// PartialTypes, given no context, reads.
	caches *runCaches
}

// runCaches returns the caches of the run Init was given, or those of the
// calls outside any run (see ratelimit.Default) before Init.
func (p *Provider) runCaches() *runCaches {
	if p.caches == nil {
		return cachesOf(context.Background())
	}
	return p.caches
}

// Init takes the region (args[0]: global, the default, or a region's
// name), the project (args[1], else GOOGLE_CLOUD_PROJECT) and the provider
// type (args[2]). A region other than global is looked up through the
// Compute Engine API, with ctx, so that a misspelt --regions fails rather
// than finding nothing. A project that hasn't enabled the Compute Engine
// API, such as one with only serverless services, can't have it looked
// up: a name shaped like a region's is then taken as given, and logged.
// The provider keeps ctx's run's caches (see cachesOf).
func (p *Provider) Init(ctx context.Context, args []string) error {
	p.caches = cachesOf(ctx)
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
	switch {
	case err == nil:
	case isServiceDisabled(err) && regionName.MatchString(region):
		log.Printf("google: region %s of project %s can't be checked, as the project hasn't enabled the Compute Engine API; importing it as given", region, project)
		name = region
	default:
		return fmt.Errorf("google: region %s of project %s: %w", region, project, err)
	}
	p.region = compute.Region{Name: name}
	return nil
}

// regionName matches the name of a region, such as us-central1, and not
// a zone's or a multi-region's.
var regionName = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)

// regionNames holds the regions lookUpRegion found, by project and region:
// every service of an Import call initialises a provider of its own, and
// they all ask about the same region.
var regionNames sync.Map

// lookUpRegion returns the name of region as the Compute Engine API knows
// it in project, asking with ctx the first time only; a failure isn't
// kept.
func lookUpRegion(ctx context.Context, project, region string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := project + "/" + region
	if name, ok := regionNames.Load(key); ok {
		return name.(string), nil
	}
	name, err := askRegion(ctx, project, region)
	if err != nil {
		return "", err
	}
	regionNames.Store(key, name)
	return name, nil
}

// askRegion asks the Compute Engine API for region in project.
func askRegion(ctx context.Context, project, region string) (string, error) {
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
		Settings: listerSettings{region: p.region, project: p.projectName, catalogue: p.catalogue, unwanted: p.unwanted}.args(),
	})
	return nil
}

// SetUnwantedTypes takes what reports the types the import leaves out
// whatever is listed, which the listers get: they skip the calls that
// list only those (see wants).
func (p *Provider) SetUnwantedTypes(unwanted func(typ string) bool) {
	p.unwanted = unwanted
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

// Profile describes Google Cloud: an import covers a project; roots go
// under <out>/google/, and terraform-google-modules are its curated
// modules, with --beta too; the services may list at once, as each has
// its own lister and API clients, and they share only the rate limiter,
// which is safe for concurrent use; resources take labels, which the provider's
// default_labels argument applies to all, and labels starting with goog-
// are Google's own, such as goog-terraform-provisioned, which the
// provider adds unless add_terraform_attribution_label is false: they
// stay on the resource, outside the configuration. Since the provider's
// version 5.0, labels holds only those of the configuration, which an
// import leaves empty, and effective_labels every label the resource has.
// A resource takes the provider block's project and region (and a zone,
// though the block sets none) as its own when it doesn't set them. Cloud
// Asset Inventory's types go in files by their family, and a root that
// environments share reads the project from the provider.
func (*Provider) Profile() harvest.Profile {
	return harvest.Profile{
		Command: providerName,
		Scope: harvest.Scope{
			Noun:     "project",
			Field:    "projects",
			Needs:    []string{"projects", "organization", "folders"},
			Flags:    []string{"regions", "projects", "organization", "folders"},
			Describe: describeScope,
		},
		Modules:            providerName,
		ModulesName:        "terraform-google-modules",
		ConcurrentListing:  true,
		DefaultTags:        &harvest.DefaultTags{Attribute: "labels", Block: "default_labels", Argument: true, ReservedPrefix: "goog-"},
		EffectiveTags:      &harvest.EffectiveTags{Attribute: "labels", Effective: "effective_labels", ReservedPrefix: "goog-"},
		InheritedArguments: []string{"project", "region", "zone"},
		Layout: harvest.Layout{
			InventoryServices: []string{"cloud_asset"},
			TypeNameTrim:      "compute_",
			NetworkTypes: []string{
				"address", "firewall", "global_address", "ha_vpn_gateway", "interconnect_attachment", "network",
				"route", "router", "subnetwork", "vpn_gateway", "vpn_tunnel",
			},
			TypeFamilies: map[string]string{
				"bigquery": "bigquery", "cloud_run": "cloud_run", "cloudfunctions": "cloudfunctions", "cloudfunctions2": "cloudfunctions",
				"container": "container", "dns": "dns", "kms": "kms", "logging": "logging", "monitoring": "monitoring",
				"project": "project", "project_iam": "iam", "pubsub": "pubsub", "secret_manager": "secret_manager",
				"service_account": "iam", "sql": "cloudsql", "storage": "gcs",
			},
		},
		Merge: harvest.Merge{
			ScopeData:  "google_client_config.current",
			ScopeLocal: "project_id",
			ScopeExpr:  "data.google_client_config.current.project",
		},
	}
}

// describeScope says what the google command reads with flags: the
// projects of --projects, and every active project of --organization and
// --folders, in the regions of --regions, global by default as for the
// command.
func describeScope(flags map[string][]string) string {
	var parts, owners []string
	if projects := flags["projects"]; len(projects) > 0 {
		parts = append(parts, "the google projects "+strings.Join(projects, ", "))
	}
	if organization := flags["organization"]; len(organization) > 0 {
		owners = append(owners, "organization "+organization[0])
	}
	if folders := flags["folders"]; len(folders) > 0 {
		owners = append(owners, "folders "+strings.Join(folders, ", "))
	}
	if len(owners) > 0 {
		parts = append(parts, "every active google project of "+strings.Join(owners, " and of "))
	}
	regions := flags["regions"]
	if len(regions) == 0 {
		regions = []string{globalRegion}
	}
	return strings.Join(parts, " and ") + " in " + strings.Join(regions, ", ")
}

// ModuleProviders configures the counterpart of the root's provider,
// which curated modules create resources with besides it, for the same
// project and region as the import: google-beta, which some modules
// name, for a root of google, and google, which every module's google_*
// resources use unless they name google-beta, for a root of google-beta
// (with --beta).
func (p *Provider) ModuleProviders() map[string]harvest.ModuleProvider {
	if p.providerType != "" {
		return map[string]harvest.ModuleProvider{
			providerName: {Source: "hashicorp/" + providerName, Config: p.providerConfig()},
		}
	}
	return map[string]harvest.ModuleProvider{
		"google-beta": {Source: "hashicorp/google-beta", Config: p.providerConfig()},
	}
}
