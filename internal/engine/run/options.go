// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import (
	"context"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// Options configure Generate.
type Options struct {
	// Config are files to write besides imports.tf, such as versions.tf and
	// providers.tf, by name.
	Config map[string][]byte
	// Omit names, per resource type, arguments to leave out of the generated
	// configuration because another imported resource manages them, such as
	// an S3 bucket's versioning. They must be computed, so that leaving them
	// out changes no plan.
	Omit map[string][]string
	// Defaults names, per resource type, the values the provider or the
	// cloud gives arguments it isn't told, as path=value, such as a
	// bucket's storage_class=STANDARD: Generate leaves out the arguments
	// set to them where the plan stays the same (see stripDefaults).
	Defaults map[string][]string
	// SchemaMetadata, if set, is what the provider's code says of its
	// resource types' arguments (see schemameta): Generate leaves out the
	// arguments set to their defaults and settles conflicting arguments
	// without a plan or a validation to tell it, where it is of the release
	// the root uses, and only tries more defaults where it isn't (see
	// schemaHints). The verification gate checks the result either way.
	SchemaMetadata *schemameta.File
	// DefaultTags, if set, lets Generate move the tags every resource shares
	// into local.tags, applied through the provider (see applyTagLift).
	DefaultTags *DefaultTags
	// EffectiveTags, if set, lets Generate write each resource's tags,
	// which an import leaves out of the provider's tags attribute, from
	// the attribute that has them all (see fillEffectiveTags).
	EffectiveTags *EffectiveTags
	// Traits are what the provider's resources and the values that
	// identify them are like, which references, owners and lifted
	// literals go by (see Traits).
	Traits Traits
	// Inherited names arguments of the provider block that resources take
	// as theirs when they don't set them, such as Google's project and
	// region: Generate leaves them out of the resources that repeat the
	// provider's value (see omitInherited).
	Inherited []string
	// StateOnly names, per resource type, arguments the provider keeps only
	// in state, which import can't set and the plan check (see runGate)
	// lets change.
	StateOnly map[string][]string
	// KeepEmpty names, per resource type, optional arguments whose empty
	// value ("", [] or {}) is a setting of its own, which Generate keeps
	// where they are set empty (see stripDefaults).
	KeepEmpty map[string][]string
	// Placeholders, per resource type, set what Terraform can't read back,
	// such as where a function's code comes from (see setPlaceholders).
	Placeholders map[string]Placeholder
	// Lifecycles, per resource type, are lifecycle settings every resource
	// of the type gets, such as create_before_destroy (see setLifecycles).
	Lifecycles map[string]Lifecycle
	// SplitFiles moves the configuration out of generated.tf, once it is
	// done, into the files the imports name (see Import.File).
	SplitFiles bool
	// ModulesDir, if set, is where Generate puts local modules for clusters
	// of resources that repeat with the same shape (see modularize).
	ModulesDir string
	// Modules counts the roots that call each module of ModulesDir (see
	// ModuleRefs): roots that share ModulesDir, such as those of one
	// import, share it, so that one's undo leaves the modules the others
	// call. nil gives the run one of its own.
	Modules *ModuleRefs `json:"-"`
	// Adapters map clusters of resources onto calls of curated modules,
	// tried before generated local modules (see synthesize).
	Adapters []adapters.Adapter
	// ModuleProviders configures, by local name, the providers modules may
	// use besides the root's, such as google-beta, or google for a root of
	// google-beta: a call whose adapter names one (see
	// adapters.Adapter.Providers) gets its provider block and source,
	// consistent with the root's provider, with its default tags where
	// the root's types are named after it (see shareDefaultTags). So does
	// a root that imports resources whose type one of them implies, such
	// as azapi_resource (see resourceProviders).
	ModuleProviders map[string]rootfile.Provider
	// External are resources the import listed but doesn't manage, which
	// the configuration may refer to through the data sources in
	// DataSources, by resource type (see addDataSourcesSkipping).
	External    []External
	DataSources map[string]DataSource
	// OtherRoots are the resources the import's other roots manage: what
	// no data source reads of them, the configuration refers to through
	// variables (see addRootVariables). ARNs say, by resource type, how the
	// ARNs of types whose IDs aren't ARNs name their resources.
	OtherRoots []RootResource
	ARNs       map[string]ARN
	// Scope, if set, is the root's own account (or subscription, or
	// project) and region: the literals that hold them read them from
	// locals of data sources instead, as a root environments share does
	// (see liftScope). Nil leaves them literal.
	Scope *ScopeLocals
	// Scanners run in the verification gate (see runScanners), each in one
	// of ScannerSlots: roots checked at the same time, such as those of one
	// import, share them, so that the scanners of all of them together
	// don't outnumber the CPUs. nil gives the run slots of its own.
	Scanners     []gate.Scanner
	ScannerSlots *gate.ScannerSlots `json:"-"`
	// IncludeOwned keeps the resources whose tags say another tool
	// manages them, which Generate leaves out otherwise (see OwnedBy).
	IncludeOwned bool
	// KeepOwned are resources, as "<type> <import ID>", to keep although
	// their tags say another tool manages them, such as those a selection
	// file includes.
	KeepOwned map[string]bool
	// MayBeGone are resources, as "<type> <import ID>", listed from an
	// index that can go on listing a resource after it is deleted, such as
	// Azure Resource Graph, by the ID the cloud itself reads them by: one
	// whose import finds no object is gone, not wrongly identified, and
	// goes in Result.Gone rather than rejected.hcl.
	MayBeGone map[string]bool
	// TidyLabels has Generate label resources the way a person names those
	// of one root (see tidyLabels): without the words of their type, without
	// a prefix most of their names share, and main for the only resource of
	// a type. Without it, labels are the names as Label writes them.
	TidyLabels bool
	// Previous, if set, is what an earlier import generated in the root,
	// which Generate generates again: each resource keeps its label while
	// its name still gives it (see relabelled), and Generate writes a
	// moved block into moves.tf for each that gets another address (see
	// moves).
	Previous *Previous
	// Arrivals are the resources the root imports that other roots of the
	// output hand over (see Removal), for its README.
	Arrivals []rootfile.Arrival
	// CommandTimeout bounds each Terraform command Generate, Add and
	// Verify run, such as a plan whose provider retries an unreachable
	// endpoint: a command still running then is stopped, and fails the
	// root. 0 means no limit.
	CommandTimeout time.Duration
	// Refine, if set, edits the root once Generate has written every file and
	// the resources' addresses are final, and before the gate, which then
	// checks what it changed as it checks the rest: an edit that changed a
	// plan fails the plan check. It is given the root's directory, what
	// the root imports and where (see ImportedAddresses), and the providers'
	// schemas, and returns warnings for the result. The importer uses it to
	// carry a CloudFormation stack's template into the root (AWS
	// --cfn-stack).
	Refine func(ctx context.Context, dir string, addresses []Managed, schemas *tfjson.ProviderSchemas) ([]string, error) `json:"-"`
	// ImportOnce, if set, has Generate import the root's resources once
	// into a private state snapshot, which its later plans read (see
	// ImportOnce). The importer sets it unless INFRAHARVEST_IMPORT_ONCE
	// switches it off: the output is the same with and without it.
	ImportOnce *ImportOnce `json:"-"`
	// RefineKey identifies what Refine carries into the root, such as the
	// templates and parameter values of its stacks, for the fingerprint
	// --resume compares: a root whose Refine input changed is generated
	// again.
	RefineKey string `json:"-"`
}

// Lifecycle is what every resource of a type gets in its lifecycle block,
// such as create_before_destroy for an API Gateway deployment, which a
// stage refers to: Reason goes in a comment above it. Lifecycle settings
// change how Terraform replaces a resource, never what the plan of an
// import shows.
type Lifecycle struct {
	CreateBeforeDestroy bool
	Reason              string
}

// Placeholder is for configuration Terraform can't read back from the
// cloud, such as where a Lambda function's code comes from: when a
// resource sets none of OneOf, setPlaceholders sets the arguments in Set
// and makes Terraform ignore changes to Ignore, so the plan keeps what is
// deployed. Reason goes in a comment above ignore_changes.
type Placeholder struct {
	OneOf  []string
	Set    map[string]string
	Ignore []string
	Reason string
}

// ImportOnce has Generate import the resources of a root once, into a
// private state snapshot, and plan the rest of the generation against it
// (see tfrun.Snapshot), instead of importing and reading every resource
// in each of the 10 to 40 plans of a root. The verification gate still
// plans the root as the user will, with import blocks and no state.
type ImportOnce struct {
	// NewRunner returns a Terraform runner for the private copy of the
	// root the snapshot is built in, as the root's own: it plans with the
	// same fresh credentials. It must return a tfrun.StateTerraform, as
	// *tfrun.Runner is, for the snapshot to be built.
	NewRunner gate.NewRunner
}
