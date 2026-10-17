// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package providers

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// DocsURL is the address of the documentation site, which flag help points
// to: a page's address is DocsURL followed by its name and a slash, such as
// DocsURL + "usage/#filtering".
const DocsURL = "https://ignatg.github.io/infraharvest/"

// A provider command is described by a definition, which command turns
// into the cobra command import and discover add. When the command runs,
// the definition's planner reads the flags into a plan: the scopes to
// import (accounts, subscriptions or projects) and, for each, the Import
// calls that cover it. run then makes those calls.

// definition describes one provider's command.
type definition struct {
	// name is the command's name, under which the provider registers.
	name string
	// newProvider returns a new provider. Every Import
	// call gets one of its own.
	newProvider func() harvest.Provider
	// short and long are the command's help.
	short, long string
	// sampleServices and sampleIDs are examples of --services and --id
	// values, for the help of the flags every provider has.
	sampleServices, sampleIDs string
	// parallelScopes says what --parallel counts, such as "accounts of
	// --accounts or --organization".
	parallelScopes string
	// flags adds the provider's own flags to the command's persistent
	// flags, each bound to a field of options or to a value of the
	// provider's own, and returns the planner that reads them.
	flags func(flags *pflag.FlagSet, options *importer.Options) planner
}

// planner checks the options and the provider's own flags, expands them
// into the scopes to import, and returns the plan, before anything is
// imported.
type planner func(ctx context.Context, options importer.Options) (plan, error)

// plan is what one run of a provider command imports.
type plan struct {
	// kind names one of scopes in logs and failures: "account",
	// "subscription" or "project".
	kind string
	// scopes are the accounts, subscriptions or projects to import, each
	// through importer.ImportEach. Without any, calls covers the one scope
	// the credentials reach, and is passed "" for it.
	scopes []string
	// calls returns the Import calls that cover one scope, in order, from
	// the options importer.ImportEach hands that scope; importer.ImportCalls
	// makes them.
	calls func(options importer.Options, scope string) []call
}

// call is one importer.Import call.
type call struct {
	options importer.Options
	// args are what the provider's Init reads. They also name the call's
	// inventory (see --reuse-inventory), so a call must have the same args
	// under discover and import.
	args []string
	// where says what the call covers, such as "region eu-west-1", for the
	// log; empty logs nothing.
	where string
	// first makes the call go before the calls of its scope that run at
	// once (see importer.Call).
	first bool
}

// importFunc is the signature of importer.ImportCalls, which tests
// replace.
type importFunc func(ctx context.Context, calls []importer.Call) error

// run makes the calls of each of p's scopes with importCalls, each call
// with a new provider from newProvider: those of one scope together, which
// stop at the first that fails (see importer.ImportCalls), and several
// scopes through importer.ImportEach, which records a scope that fails and
// goes on to the next.
func (p plan) run(ctx context.Context, options importer.Options, newProvider func() harvest.Provider, importCalls importFunc) error {
	importScope := func(scopeOptions importer.Options, scope string) error {
		planned := p.calls(scopeOptions, scope)
		calls := make([]importer.Call, len(planned))
		for i, c := range planned {
			calls[i] = importer.Call{Provider: newProvider(), Options: c.options, Args: c.args, Where: c.where, First: c.first}
		}
		return importCalls(ctx, calls)
	}
	if p.scopes == nil {
		return importScope(options, "")
	}
	return importer.ImportEach(options, p.kind, p.scopes, importScope)
}

// define registers the provider d describes.
func define(d definition) {
	register(d.name, d.command, d.newProvider)
}

// command returns d's command, whose flags write into its own copy of
// options: import passes zero options, discover options with Discover set.
func (d definition) command(options importer.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   d.name,
		Short: d.short,
		Long:  d.long,
	}
	BaseFlags(cmd.PersistentFlags(), &options, d.sampleServices, d.sampleIDs)
	ParallelFlag(cmd, &options, d.parallelScopes)
	planScopes := d.flags(cmd.PersistentFlags(), &options)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		// The command's context reaches every Import call, so that an
		// interrupt stops the import.
		ctx := c.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		if err := importer.CheckOptions(d.newProvider(), options); err != nil {
			return err
		}
		p, err := planScopes(ctx, options)
		if err != nil {
			return err
		}
		return p.run(ctx, options, d.newProvider, importer.ImportCalls)
	}
	return cmd
}

// BaseFlags adds the flags every provider command has to flags, bound to
// options. Their names are also the keys of a configuration file's
// settings. sampleRes and sampleIDs are examples of --services and --id
// values for the help.
func BaseFlags(flags *pflag.FlagSet, options *importer.Options, sampleRes, sampleIDs string) {
	// What to list.
	flags.StringSliceVarP(&options.Services, "services", "s", nil, "services to import, such as "+sampleRes+`, or "*" for all (infraharvest services <provider> lists them)`)
	flags.StringSliceVar(&options.Skip, "skip", nil, "services to leave out of --services, such as "+sampleRes)
	flags.StringArrayVar(&options.IDFilters, "id", nil, "keep only the resources of a type with these IDs, as <type>=<id>[,<id>...] such as "+sampleIDs+"; repeat for other types (see "+DocsURL+"usage/#filtering)")
	flags.StringArrayVar(&options.TagFilters, "tag", nil, "keep only the resources with this tag, as <key>[=<value>[,<value>...]] such as env=prod, where the service's lister supports it (see "+DocsURL+"usage/#filtering)")
	flags.StringArrayVar(&options.WhereFilters, "where", nil, "keep only the resources whose attribute at a dotted path has one of these values, as <path>[=<value>[,<value>...]], where the service's lister supports it (see "+DocsURL+"usage/#filtering)")
	flags.BoolVarP(&options.Verbose, "verbose", "v", false, "log more, such as the requests to the cloud's APIs")
	flags.DurationVar(&options.ListTimeout, "list-timeout", importer.DefaultListTimeout, "longest time to list one service in one account and region, subscription or project; a service that takes longer is reported as failed (0: no limit)")
	flags.IntVar(&options.ListParallel, "list-parallel", importer.DefaultListParallel, "how many services of an account and region, a subscription or a project to list at once")
	flags.Float64Var(&options.RateLimit, "rate-limit", ratelimit.DefaultRate, "most requests a second to each cloud API: per API host, and for AWS and Azure per account or subscription too (0: no limit)")

	// Where the output goes, and how it is generated.
	flags.StringVar(&options.Layout, "layout", importer.DefaultLayout, "directories of the generated roots, one root each, from {out}, {provider} (aws, azure or google), {account}, {region}, {service} and {env} (see --env-tags)")
	flags.StringVarP(&options.Out, "out", "o", importer.DefaultOut, "directory to write the generated roots and the report to")
	flags.StringVar(&options.Format, "format", importer.FormatText, "text, or json to also print the import report as JSON on stdout")
	flags.StringSliceVar(&options.EnvTags, "env-tags", nil, "tag or label keys, such as Environment, whose value names a resource's environment: roots per environment, under {out}/envs/{env}/, for {env} in --layout (default for {env}: Environment, environment, Env, env)")
	flags.StringSliceVar(&options.ReportTags, "report-tags", nil, "tag keys, such as owner,team, to count what was discovered, imported and managed by in the report, one table per key")
	flags.StringVar(&options.Modules, "modules", importer.ModulesRegistry, "registry moves clusters of resources into curated public modules (terraform-aws-modules, terraform-google-modules and Azure Verified Modules), at the release each adapter is tested with, where the plan stays the same, else into generated local modules; latest-untested calls each module's newest release instead; local uses generated local modules only; none keeps every resource in the root")
	flags.StringVar(&options.Catalogue, "catalogue", "", "directory of catalogue files (YAML) that add to or override what the built-in catalogue says about resource types: import IDs, arguments to leave out, default skips, data sources (see "+DocsURL+"catalogue/)")
	flags.StringVar(&options.Engine, "engine", importer.EngineTerraform, "terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks")
	flags.StringVar(&options.TerraformPath, "terraform-path", "", "Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6)")
	flags.DurationVar(&options.EngineTimeout, "engine-timeout", importer.DefaultEngineTimeout, "longest time one Terraform or OpenTofu command, such as the plan of one root, may run; a root whose command takes longer fails (0: no limit)")

	// Which resources to import, and how earlier runs count.
	flags.StringVar(&options.Selection, "selection", "", "selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml)")
	flags.BoolVar(&options.All, "all", false, "import everything the default selection includes, without a selection file")
	flags.StringSliceVar(&options.ManagedState, "managed-state", nil, "leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...&profile=...] (read with --profile unless it names one) gs://bucket/prefix or https://<account>.blob.core.windows.net/<container>/prefix (all their .tfstate objects), or tfc://<organization>/<workspace> (<prefix>* for several, ?host= for Terraform Enterprise); backend reads the configured S3, GCS or azurerm backend's state")
	flags.BoolVar(&options.IncludeOwned, "include-owned", false, "import resources whose tags or labels say another Terraform state or tool manages them (ManagedBy=Terraform, Pulumi, CloudFormation ..., aws:cloudformation:stack-name, goog-terraform-provisioned), which are left out by default")
	flags.BoolVar(&options.AllowPartial, "allow-partial", false, "keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1")
	flags.BoolVar(&options.Resume, "resume", false, "skip the roots a previous run generated from the same resources and options, such as after a run that failed part way")
	flags.BoolVar(&options.Incremental, "incremental", false, "add what is new to the roots earlier imports generated in --out, after the blocks of the file of its type, without changing what they have")
	flags.BoolVar(&options.ReuseInventory, "reuse-inventory", false, "import from the resources infraharvest discover listed into the same --out, instead of listing them again")
	flags.BoolVar(&options.ForceUnlock, "force-unlock", false, "write to --out even when another run holds its lock, as when that run is stuck, or when the file system can't lock; a run that is killed releases the lock by itself")
}

// ParallelFlag adds --parallel to cmd, bound to options.Parallel: how many
// of the scopes what describes import at once.
func ParallelFlag(cmd *cobra.Command, options *importer.Options, what string) {
	cmd.PersistentFlags().IntVar(&options.Parallel, "parallel", 1, "how many "+what+" to import at once, in all; each runs its own Terraform, so memory use grows with it")
}

// checkScopesApart fails when several scopes of kind would share roots:
// pattern names no {account} to put each in directories of its own. An
// empty pattern leaves the layout to the provider.
func checkScopesApart(pattern, kind string) error {
	if pattern != "" && !strings.Contains(pattern, "{account}") {
		return fmt.Errorf("--layout %q needs {account} to keep several %ss apart", pattern, kind)
	}
	return nil
}

// appendMissing returns scopes followed by those of found it doesn't
// already hold, in found's order. scopes itself is left as it is.
func appendMissing(scopes, found []string) []string {
	merged := slices.Clone(scopes)
	for _, scope := range found {
		if !slices.Contains(merged, scope) {
			merged = append(merged, scope)
		}
	}
	return merged
}
