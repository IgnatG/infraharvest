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

// ProviderWithRegistrySource is a provider that knows its Terraform
// registry source address, such as hashicorp/aws, for the
// required_providers block.
type ProviderWithRegistrySource interface {
	RegistrySource() string
}

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

// ProviderWithOptInServices is a provider with services that the wildcard
// --services=* leaves out, because they list another way what other
// services already list. They are listed only when named.
type ProviderWithOptInServices interface {
	OptInServices() []string
}

// ProviderWithTags is a provider that reads the tags of listed resources
// whose listers recorded none (see AttributeTags). The result is keyed
// "<type> <id>"; resources without known tags are left out of it. Tags feed
// the selection rules, the picker and the report.
type ProviderWithTags interface {
	Tags(ctx context.Context, resources []Resource) (map[string]map[string]string, error)
}

// ProviderWithDirectory is a provider whose roots go into a directory not
// named after its Terraform provider (Name): Directory is the {provider}
// of --layout and the first part of the selection file's scope. It is the
// name of the provider's command, such as azure for azurerm and google for
// google-beta, so that the output is laid out by what the user typed.
// Without it, the directory is Name.
type ProviderWithDirectory interface {
	Directory() string
}

// ProviderWithScope is a provider that knows the account (or subscription,
// or project) and the region an import covers. Output is laid out as
// {out}/{provider}/{account}/{region}/, one root per state boundary.
type ProviderWithScope interface {
	Scope(ctx context.Context) (account, region string, err error)
}

// ProviderWithDefaultTags is a provider that can apply tags to every
// resource, so the engine can move the tags all resources share into the
// provider configuration. It returns the resources' tag attribute, the
// provider-level block that applies tags (default_tags on AWS; empty when
// the provider has none, as with azurerm, in which case the shared tags go
// to a local instead), and the key prefix of tags the cloud reserves, which
// cannot be applied that way.
type ProviderWithDefaultTags interface {
	DefaultTags() (attribute, block, reservedPrefix string)
}

// ProviderWithDefaultTagsArgument is a provider whose default-tags "block"
// from DefaultTags is in fact a map argument of the provider block, like
// Google's default_labels, when DefaultTagsArgument returns true.
type ProviderWithDefaultTagsArgument interface {
	DefaultTagsArgument() bool
}

// ProviderWithConcurrentListing is a provider whose services may list at the
// same time, because their listers share only state that is safe for
// concurrent use, when ConcurrentListing returns true.
type ProviderWithConcurrentListing interface {
	ConcurrentListing() bool
}

// ProviderWithTerraformEnv is a provider that gives the Terraform CLI
// environment variables it needs to import the provider's resources, such
// as credentials resolved from an SSO session or an MFA-assumed role that
// Terraform cannot resolve on its own. It is called again for every root,
// so it may hand out refreshed credentials.
type ProviderWithTerraformEnv interface {
	TerraformEnv(ctx context.Context) (map[string]string, error)
}
