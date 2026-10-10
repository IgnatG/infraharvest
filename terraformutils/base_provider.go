// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package terraformutils

import (
	"context"
)

type ProviderGenerator interface {
	Init(args []string) error
	InitService(serviceName string, verbose bool) error
	GetName() string
	GetService() ServiceGenerator
	GetSupportedService() map[string]ServiceGenerator
	GetProviderData(arg ...string) map[string]interface{}
}

type ProviderWithSource interface {
	GetSource() string
}

// ProviderWithImportIDs maps a listed resource to the ID Terraform imports it
// with, for listers that record a different ID. ok is
// false for resource types Terraform can't import.
type ProviderWithImportIDs interface {
	ImportID(r Resource) (id string, ok bool)
}

// ProviderWithChildImports lists resources that
// hold parts of r's configuration the provider manages as separate
// resources, such as an S3 bucket's versioning. They are imported next to
// r. It returns no children for most resources.
type ProviderWithChildImports interface {
	ChildImports(ctx context.Context, r Resource) ([]Resource, error)
}

// ProviderWithOmittedArguments names, per resource type or under "*" for
// all, arguments that generated configuration must
// leave out: arguments a child resource (see ProviderWithChildImports)
// manages, or that only repeat what the provider configuration sets.
// They must be computed, so that leaving them out changes no plan.
type ProviderWithOmittedArguments interface {
	OmittedArguments() map[string][]string
}

// ProviderWithStateOnlyArguments names, per resource type, arguments the
// provider keeps only in state: AWS doesn't report them, so they are unset
// after an import and the first apply records their defaults without
// calling the cloud. The verification gate lets plans change them.
type ProviderWithStateOnlyArguments interface {
	StateOnlyArguments() map[string][]string
}

// ProviderWithSelectionDefaults names the listed resources a selection
// leaves out unless told otherwise, by "type id", with the reason:
// resources the cloud creates and manages itself, such as a default VPC.
type ProviderWithSelectionDefaults interface {
	ExcludedByDefault(ctx context.Context, resources []Resource) (map[string]string, error)
}

// ProviderWithOptInServices names services --resources=* leaves out,
// because they list what other services do, another way: they are listed
// only when named.
type ProviderWithOptInServices interface {
	OptInServices() []string
}

// ProviderWithTags reads the tags of listed resources whose listers don't
// record them (see AttributeTags), for selection rules, the picker and the
// report. It returns them by "type id", the lister's ID, and leaves out
// resources it has no tags for.
type ProviderWithTags interface {
	Tags(ctx context.Context, resources []Resource) (map[string]map[string]string, error)
}

// ProviderWithScope names the account (or subscription or project) and the
// region an import covers, for the output layout
// ({output}/{provider}/{account}/{region}/): one root per state
// boundary.
type ProviderWithScope interface {
	Scope(ctx context.Context) (account, region string, err error)
}

// ProviderWithDefaultTags describes how the provider applies tags to every
// resource, for the engine to move the tags all resources share
// into the provider configuration: the resources' tags attribute, the
// provider block that applies tags (AWS: default_tags), and the key prefix
// of tags the cloud sets itself, which can't be applied that way.
type ProviderWithDefaultTags interface {
	DefaultTags() (attribute, block, reservedPrefix string)
}

// ProviderWithDefaultTagsArgument says whether the block DefaultTags names
// is an argument of the provider block that holds the tags, as the Google
// provider's default_labels, rather than a block holding them.
type ProviderWithDefaultTagsArgument interface {
	DefaultTagsArgument() bool
}

// ProviderWithTerraformEnv gives Terraform the environment it needs to
// import the provider's resources, such as the credentials the provider
// lists with, which Terraform may not resolve on its own (an SSO session,
// a role assumed with an MFA code). It is asked again for each root, so
// it can return fresh credentials.
type ProviderWithTerraformEnv interface {
	TerraformEnv(ctx context.Context) (map[string]string, error)
}

// Provider holds the service a provider generator initialised. Providers
// embed it and implement the rest of ProviderGenerator themselves, so a
// missing method fails to compile instead of panicking at run time.
type Provider struct {
	Service ServiceGenerator
}

func (p *Provider) GetService() ServiceGenerator {
	return p.Service
}

// DataSource reads one resource: the data source's type and the argument
// that takes the resource's import ID.
type DataSource struct {
	Type, Argument string
}

// ProviderWithDataSources names, per resource type, the data source that
// reads one resource by its import ID, for the engine to refer to
// resources the selection leaves out, such as a default VPC.
type ProviderWithDataSources interface {
	DataSources() map[string]DataSource
}
