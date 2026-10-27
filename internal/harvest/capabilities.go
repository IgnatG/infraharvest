// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"context"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// The interfaces in this file are capabilities a Provider may have
// beside the required methods. The importer checks for each one with a type
// assertion and falls back to doing without when a provider lacks it.
//
// Several of them key their results by "<type> <id>": the Terraform resource
// type, one space, and the ID the lister recorded (Observation.ID).

// ProviderWithChildImports is a provider that, for a listed resource r,
// finds the separately managed Terraform resources that hold parts of r's
// configuration (an S3 bucket's versioning or encryption, for example), so
// that they are imported beside it. Only the child types the catalogue
// declares for r's type are returned, and usually there are none.
type ProviderWithChildImports interface {
	ChildImports(ctx context.Context, r Resource) ([]Resource, error)
}

// ProviderWithSelectionDefaults is a provider that names, among resources,
// those a selection leaves out unless the user asks for them, when telling
// them apart takes a cloud API call (the default VPC, for example). The map
// is keyed "<type> <id>" and holds a reason a person can read. Rules that
// an ID or the attributes alone decide belong in the catalogue instead.
type ProviderWithSelectionDefaults interface {
	ExcludedByDefault(ctx context.Context, resources []Resource) (map[string]string, error)
}

// ProviderWithUnwantedTypes is a provider whose listers can skip the calls
// that list only resources of types the import leaves out whatever is
// listed (see selection.Unwanted), such as an AWS IAM user's access keys.
// SetUnwantedTypes is called before the provider makes its listers.
type ProviderWithUnwantedTypes interface {
	SetUnwantedTypes(types []string)
}

// ProviderWithCatalogue is a provider that is given the catalogue in effect
// for the import (the built-in one with any --catalogue overlay), before
// any of its services lists. Services that turn inventory-API results into
// Terraform types look the types up in it.
type ProviderWithCatalogue interface {
	SetCatalogue(c *catalogue.Catalogue)
}

// ModuleProvider is a provider other than the import's own that a module
// call may need: its registry source address and the arguments of its
// provider block.
type ModuleProvider struct {
	Source string
	Config map[string]interface{}
}

// ProviderWithModuleProviders is a provider whose curated modules use
// further providers. The map is keyed by the providers' local names, such
// as google-beta, and configures each to target the same project or
// subscription as the import. A root only receives the ones its module
// calls need.
type ProviderWithModuleProviders interface {
	ModuleProviders() map[string]ModuleProvider
}

// ProviderWithListedByOthers is a provider with dependent services (see
// Profile.DependentServices). Before they list, the importer gives each
// one's provider, with SetListedByOthers, what the import's other services
// listed: also what a service that failed in part listed, and nothing of
// one that failed.
type ProviderWithListedByOthers interface {
	SetListedByOthers(resources []Resource)
}

// ProviderWithTags is a provider that reads the tags of listed resources
// whose listers recorded none (see AttributeTags). The result is keyed
// "<type> <id>"; resources without known tags are left out of it. Tags feed
// the selection rules, the picker and the report.
type ProviderWithTags interface {
	Tags(ctx context.Context, resources []Resource) (map[string]map[string]string, error)
}

// ProviderWithScope is a provider that knows the account (or subscription,
// or project) and the region an import covers. Output is laid out as
// {out}/{provider}/{account}/{region}/, one root per state boundary.
type ProviderWithScope interface {
	Scope(ctx context.Context) (account, region string, err error)
}

// ProviderWithTerraformEnv is a provider that gives the Terraform CLI
// environment variables it needs to import the provider's resources, such
// as credentials resolved from an SSO session or an MFA-assumed role that
// Terraform cannot resolve on its own. It is called again for every root,
// so it may hand out refreshed credentials.
type ProviderWithTerraformEnv interface {
	TerraformEnv(ctx context.Context) (map[string]string, error)
}

// ProviderWithPartialTypes is a provider whose listing of its scope can
// leave out resources of some types that are still there: those it lists
// in another scope, such as the subnetworks of a Google Cloud region, which
// the import of global lists with their networks, or where something that
// can change puts them, and those it couldn't look for. An incremental
// import doesn't take a resource of those types that the listing didn't
// find for one deleted. PartialTypes is asked after the listing.
type ProviderWithPartialTypes interface {
	PartialTypes() []string
}

// ProviderWithImportScope is a provider told to import only some of what
// it lists, such as the resources of the CloudFormation stacks AWS's
// --cfn-stack names. ImportScope is asked once the listing is done, with
// every listed resource, and returns nil when the import has no scope.
type ProviderWithImportScope interface {
	ImportScope(ctx context.Context, resources []Resource) (*ImportScope, error)
}

// ImportScope is what a provider's scope makes of the listed resources,
// keyed "<type> <id>": Inside are those the scope names, which the import
// takes whatever the default selection says of another tool managing them,
// as the scope is that tool's; Outside are the others, with the reason,
// which the import leaves out whatever a selection file says.
type ImportScope struct {
	Inside  map[string]bool
	Outside map[string]string
}
