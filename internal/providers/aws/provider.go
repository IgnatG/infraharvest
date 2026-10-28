// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
	"github.com/IgnatG/infraharvest/internal/report/cfn"
)

// The capabilities Provider has, checked when it builds: one whose method
// no longer matches its interface would otherwise be dropped in silence,
// as the importer asserts them at run time.
var (
	_ harvest.ProviderWithChildImports      = (*Provider)(nil)
	_ harvest.ProviderWithSelectionDefaults = (*Provider)(nil)
	_ harvest.ProviderWithImportScope       = (*Provider)(nil)
	_ harvest.ProviderWithPartialTypes      = (*Provider)(nil)
	_ harvest.ProviderWithScope             = (*Provider)(nil)
	_ harvest.ProviderWithCatalogue         = (*Provider)(nil)
	_ harvest.ProviderWithUnwantedTypes     = (*Provider)(nil)
	_ harvest.ProviderWithStackMapper       = (*Provider)(nil)
	_ harvest.ProviderWithTags              = (*Provider)(nil)
	_ harvest.ProviderWithTerraformEnv      = (*Provider)(nil)
	_ harvest.ProviderWithQueryScope        = (*Provider)(nil)
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
	// account and resolvedRegion are the account the import covers and,
	// for ProfileRegion, the region the SDK resolved, once Scope has found
	// them; the provider block pins both.
	account, resolvedRegion string
	// cat is the catalogue of the import, nil until SetCatalogue sets it.
	cat *catalogue.Catalogue
	// unwanted reports the types the import leaves out whatever is listed
	// (see SetUnwantedTypes); nil wants every type.
	unwanted func(typ string) bool
	// stackScope, when set, scopes the import to the resources of
	// CloudFormation stacks (see SetStackScope).
	stackScope *StackScope
	// scopedStacks are the stacks of stackScope as ImportScope last
	// matched them with what is listed (see CloudFormationStacks).
	scopedStacks []cfn.Stack
	// limiter is the limiter of the run Init was given (see
	// ratelimit.FromContext), whose configurations the provider's own
	// calls use, whatever context they are given (see service).
	limiter *ratelimit.Limiter
}

// Init takes the region (args[0]), the profile (args[1]) and, optionally,
// a role to assume (args[2]), and the run's limiter from ctx. It contacts
// nothing, so it never fails: the credentials are resolved when the first
// service lists.
func (p *Provider) Init(ctx context.Context, args []string) error {
	p.limiter = ratelimit.FromContext(ctx)
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

// Profile describes AWS: an import covers an account, which has a
// 12-digit ID; the services of AWS services at end of support, WAF
// Classic and MediaStore, which only old estates still hold, are listed
// only when --services names them, so that every import of * doesn't ask
// their retired APIs in every region, as is the query service, which lists
// the types --query-types names and leaves the listed ones to their
// services; terraform-aws-modules are its
// curated modules; a resource's block label comes from its Name tag; the
// services of a region may list at once, as each has its own lister and
// they share only the SDK configuration, which is safe to share;
// default_tags applies tags to every resource, and keys starting with
// aws: are AWS's own; the network's services share network.tf; a root
// that environments share reads the account and the region from the
// provider, which plans each environment in its own; and the s3 backend
// keeps state in AWS.
func (*Provider) Profile() harvest.Profile {
	return harvest.Profile{
		Command: providerName,
		Scope: harvest.Scope{
			Noun:     "account",
			Field:    "accounts",
			ID:       accountID,
			IDFormat: "an account ID, 12 digits",
			Flags:    []string{"regions", "profile", "accounts", "cfn-stack", "cfn-ignore"},
			Describe: describeScope,
		},
		Modules:           providerName,
		ModulesName:       "terraform-aws-modules",
		OptInServices:     []string{"media_store", "waf", "waf_regional", harvest.QueryService},
		ListedTypes:       listedTypes,
		NameTag:           "Name",
		ConcurrentListing: true,
		DefaultTags:       &harvest.DefaultTags{Attribute: "tags", Block: "default_tags", ReservedPrefix: "aws:"},
		Layout: harvest.Layout{
			NetworkServices: []string{
				"customer_gateway", "eip", "eni", "igw", "nacl", "nat", "route_table", "sg", "subnet",
				"transit_gateway", "vpc", "vpc_endpoint", "vpc_peering", "vpn_connection", "vpn_gateway",
			},
		},
		Merge: harvest.Merge{
			ScopeData:       "aws_caller_identity.current",
			ScopeLocal:      "aws_account_id",
			ScopeExpr:       "data.aws_caller_identity.current.account_id",
			RegionArgument:  "region",
			RegionTitle:     "AWS region",
			RegionData:      "aws_region.current",
			RegionLocal:     "aws_region",
			RegionExpr:      "data.aws_region.current.region",
			EnvironmentData: environmentData,
		},
		Backend: &harvest.Backend{Type: "s3", Settings: []string{"bucket", "encrypt", "key", "kms_key_id", "region", "use_lockfile"}},
	}
}

// environmentData returns what an environment's credentials and region
// give the data sources that read its account and region: the account's
// ID, the region, and the partition the region is in, with its DNS
// suffix, where it is known (see partitionDNSSuffixes).
func environmentData(account, region string) harvest.EnvironmentData {
	data := harvest.EnvironmentData{}
	data.Set("aws_caller_identity", account, "account_id", "id")
	data.Set("aws_region", region, "region", "name", "id")
	if region == "" {
		return data
	}
	if partition := partitionOf(region); partitionDNSSuffixes[partition] != "" {
		data.Set("aws_partition", partition, "partition", "id")
		data.Set("aws_partition", partitionDNSSuffixes[partition], "dns_suffix")
	}
	return data
}

// partitionDNSSuffixes are the DNS suffixes of the partitions whose
// suffix is known: the commercial one's, China's and GovCloud's.
var partitionDNSSuffixes = map[string]string{"aws": "amazonaws.com", "aws-cn": "amazonaws.com.cn", "aws-us-gov": "amazonaws.com"}

// accountID matches an AWS account ID.
var accountID = regexp.MustCompile(`^[0-9]{12}$`)

// describeScope says what the aws command reads with flags: the account
// the credentials reach, or those of --accounts, with the credentials of
// --profile, in the regions of --regions, and only the resources of the
// stacks of --cfn-stack.
func describeScope(flags map[string][]string) string {
	text := "the aws account"
	if accounts := flags["accounts"]; len(accounts) > 0 {
		text = "the aws accounts " + strings.Join(accounts, ", ")
	}
	if profile := flags["profile"]; len(profile) > 0 {
		text += " with profile " + profile[0]
	}
	if regions := flags["regions"]; len(regions) > 0 {
		text += " in " + strings.Join(regions, ", ")
	}
	if stacks := flags["cfn-stack"]; len(stacks) > 0 {
		text += ", only the resources of CloudFormation stacks " + strings.Join(stacks, ", ")
		if ignore := flags["cfn-ignore"]; len(ignore) > 0 {
			text += ", acknowledging " + strings.Join(ignore, ", ")
		}
	}
	return text
}

// ProviderBlock returns the provider "aws" block: the region, us-east-1
// for the global services and, for ProfileRegion, the region the listing
// resolved; the account the import covers, as the only one of
// allowed_account_ids, so that a later plan with other credentials fails
// rather than reading another account's resources; and the role to assume,
// if any. The account and the resolved region are known once Scope has run,
// as it does before the configuration is generated; before that, they are
// left out, and for ProfileRegion Terraform resolves the same default
// region as the listing did.
func (p Provider) ProviderBlock() map[string]interface{} {
	config := map[string]interface{}{}
	switch p.region {
	case ProfileRegion:
		if p.resolvedRegion != "" {
			config["region"] = p.resolvedRegion
		}
	case GlobalPseudoRegion:
		config["region"] = GlobalHomeRegion
	default:
		config["region"] = p.region
	}
	if p.account != "" {
		config["allowed_account_ids"] = []string{p.account}
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
// lister per service of the cloud_control entries of the catalogue of the
// import (see SetCatalogue), which takes the place of a lister of the same
// name; and the query service's, which lists through Terraform and asks
// AWS nothing itself.
func (p *Provider) Services() map[string]harvest.Lister {
	supported := make(map[string]harvest.Lister, len(serviceTable))
	for _, s := range serviceTable {
		supported[s.name] = &guardedLister{lister: s.newLister()}
	}
	for name, types := range p.cloudControl() {
		supported[name] = &guardedLister{lister: &CloudControlLister{types: types}}
	}
	supported[harvest.QueryService] = &harvest.QueryLister{}
	return supported
}

// newServiceLister returns a new lister, wrapped in a guardedLister, for
// the service called name, as Services has it, or nil when there is no
// such service: a Cloud Control service takes the place of a lister of the
// same name. The query service's lister isn't wrapped.
func (p *Provider) newServiceLister(name string) harvest.Lister {
	if name == harvest.QueryService {
		return &harvest.QueryLister{}
	}
	if types, ok := p.cloudControl()[name]; ok {
		return &guardedLister{lister: p.withUnwanted(&CloudControlLister{types: types})}
	}
	for _, s := range serviceTable {
		if s.name == name {
			return &guardedLister{lister: p.withUnwanted(s.newLister())}
		}
	}
	return nil
}

// Select makes the service called name the provider's current one,
// with a new lister, and passes it the provider's settings. Only that
// service's lister is made (see newServiceLister).
func (p *Provider) Select(name string, verbose bool) error {
	service := p.newServiceLister(name)
	if service == nil {
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
	return serviceSettings{region: p.region, profile: p.profile, roleARN: p.roleARN}.args()
}

// service returns a Service of the provider's settings, for the provider's
// own calls with ctx, in the run Init was given: Terraform's credentials
// are asked for with the context of the generation, which carries no
// limiter, and they must come from the run's configuration all the same.
func (p *Provider) service(ctx context.Context) *Service {
	if p.limiter != nil {
		ctx = ratelimit.NewContext(ctx, p.limiter)
	}
	service := &Service{}
	service.Configure(harvest.ListerConfig{Settings: p.serviceArgs()})
	service.SetContext(ctx)
	return service
}

// SetUnwantedTypes takes what reports the types the import leaves out
// whatever is listed, such as aws_iam_access_key unless the selection
// file includes some: the listers it makes after that skip the calls that
// list only those (see Service.wants).
func (p *Provider) SetUnwantedTypes(unwanted func(typ string) bool) {
	p.unwanted = unwanted
}

// withUnwanted gives lister the types the import leaves out, if it takes
// them, and returns it.
func (p *Provider) withUnwanted(lister harvest.Lister) harvest.Lister {
	if l, ok := lister.(interface{ setUnwanted(func(typ string) bool) }); ok {
		l.setUnwanted(p.unwanted)
	}
	return lister
}
