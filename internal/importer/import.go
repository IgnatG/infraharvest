// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/discovery"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// Defaults of the flags every provider command shares.
const (
	// DefaultOut is the directory generated roots go into.
	DefaultOut = "infraharvest-out"
	// DefaultListTimeout is how long one service may list in one scope.
	DefaultListTimeout = 30 * time.Minute
	// DefaultListParallel is how many services of one scope list at once.
	DefaultListParallel = 4
	// DefaultEngineTimeout is how long one Terraform or OpenTofu command may
	// run, such as one plan of a root.
	DefaultEngineTimeout = time.Hour
)

// Options is what one Import call imports, and how. The provider commands
// fill it in from their flags; most fields are named after the flag that
// sets them.
type Options struct {
	// What to list.

	// Services are the services to import; "*" stands for all of them
	// (see ResolveServices).
	Services []string
	// Skip are services to leave out of Services.
	Skip []string
	// IDFilters, TagFilters and WhereFilters keep only the resources these
	// filters match: --id, --tag and --where, as harvest.ParseIDFilter,
	// ParseTagFilter and ParseWhereFilter read them.
	IDFilters    []string
	TagFilters   []string
	WhereFilters []string
	// Verbose logs more of what the listers do.
	Verbose bool
	// ListTimeout bounds how long one service lists; 0 means no limit.
	ListTimeout time.Duration
	// ListParallel is how many services list at once, where the provider
	// allows it.
	ListParallel int
	// RateLimit is the most requests a second to each cloud API.
	RateLimit float64

	// Where the output goes.

	// Layout lays out the generated roots (see Path).
	Layout string
	// Out is the output directory.
	Out string
	// Format is FormatText, or FormatJSON to also print the report on
	// stdout.
	Format string
	// Stdout is where FormatJSON prints the report: the command's output,
	// os.Stdout if nil.
	Stdout io.Writer
	// EnvTags are the tag keys whose value names a resource's environment.
	EnvTags []string
	// DefaultEnv is the environment of what no environment tag places,
	// instead of sharedEnv; "" for sharedEnv.
	DefaultEnv string
	// MergeEnvironments merges the roots of a stack's environments, which
	// the configuration file's environments name, into one root they share
	// (see MergeEnvironments).
	MergeEnvironments bool
	// ReportTags are the tag keys the report counts resources by.
	ReportTags []string
	// Modules says which modules hold clusters of resources (see
	// ModulesRegistry and the other Modules values).
	Modules string
	// Catalogue is a directory of catalogue files that extend the
	// built-in catalogue.
	Catalogue string
	// Engine is EngineTerraform or EngineTofu; empty means Terraform.
	Engine string
	// TerraformPath is the engine's binary; empty finds or installs it.
	TerraformPath string
	// EngineTimeout bounds each command of the engine, such as one plan of
	// a root; 0 means no limit.
	EngineTimeout time.Duration

	// Which resources to import, and how to treat earlier runs.

	// Selection is the selection file discover writes and import follows.
	Selection string
	// All imports what the default selection includes, without a file.
	All bool
	// Discover lists resources into the selection file instead of
	// importing them.
	Discover bool
	// ManagedState is state whose resources are left out, as already
	// managed by Terraform.
	ManagedState []string
	// IncludeOwned imports the resources whose tags or labels say another
	// Terraform state or tool manages them, which are left out otherwise
	// (see engine.OwnedBy).
	IncludeOwned bool
	// AllowPartial accepts output some services or resources are missing
	// from.
	AllowPartial bool
	// Resume skips the roots a previous, identical run generated, and
	// generates again those a previous run didn't finish.
	Resume bool
	// Regenerate generates again the roots earlier runs generated.
	Regenerate bool
	// Incremental adds what is new to roots earlier runs generated.
	Incremental bool
	// ReuseInventory imports from what discover listed, instead of listing
	// again.
	ReuseInventory bool
	// ForceUnlock writes to Out even when another run holds its lock, or
	// the lock can't be taken (see lockOutput).
	ForceUnlock bool

	// Where the provider lists: read by the provider commands only.

	// Profile is the AWS profile; the managed state in S3 is read with it
	// too.
	Profile string
	// Regions are the AWS or Google Cloud regions to import.
	Regions []string
	// Projects are the Google Cloud projects to import.
	Projects []string
	// ResourceGroup is the Azure resource group to import.
	ResourceGroup string
	// Accounts are the AWS accounts to import, each through AssumeRole.
	Accounts []string
	// Organization imports every active account of the AWS organization.
	Organization bool
	// AssumeRole is the role to assume, where {account} stands for each
	// account's ID.
	AssumeRole string
	// RoleARN is the role the AWS provider assumes for one call.
	RoleARN string
	// ResourceExplorer skips the AWS services Resource Explorer shows are
	// empty in a region.
	ResourceExplorer bool
	// Parallel is how many accounts, subscriptions or projects import at
	// once (see ImportEach), and how many Import calls of the command, in
	// every account and region, list or generate at once in all (see
	// ImportCalls).
	Parallel int

	// run is the run this call records into, when it isn't the command's
	// own: one of several scopes imported at once has a run of its own
	// (see ImportEach).
	run *Run
	// cat is the catalogue of the import, the built-in one with the
	// --catalogue overlay merged in, once importInto has loaded it; the
	// provider of each service listed is given it (see listServices).
	cat *catalogue.Catalogue
	// unwanted are the types the import leaves out whatever is listed (see
	// unwantedTypes), whose listing the provider of each service listed may
	// skip (see listServices).
	unwanted []string
	// tools are what the call reaches outside the process through; nil
	// uses the real ones (see callTools).
	tools *callTools
}

// Import lists the resources of provider's services in options.Services,
// for the scope args name (provider.Init reads them), and generates their
// Terraform configuration with the engine options.Engine names.
func Import(ctx context.Context, provider harvest.Provider, options Options, args []string) error {
	switch options.Engine {
	case "", EngineTerraform, EngineTofu:
		return importWithEngine(ctx, provider, options, args)
	default:
		return fmt.Errorf("unknown --engine %q: use %s or %s", options.Engine, EngineTerraform, EngineTofu)
	}
}

// CheckOptions rejects what a provider command was told that no Import
// call could do, before any account, region or project is planned: an
// unknown --engine, --format or --modules, a filter that doesn't parse,
// an import that says neither --selection nor --all, --services or --skip
// names provider has no service of, a --layout checkLayout rejects,
// --default-env without environments to split roots into, a
// negative --rate-limit, --list-timeout, --list-parallel, --parallel or
// --engine-timeout, a --catalogue directory that doesn't load, and the
// selection file and managed state sources it can tell are wrong (see
// checkSources).
// Checked once, they fail the command the same way however many scopes
// it covers, instead of failing each scope and reading as a partial
// import.
func CheckOptions(provider harvest.Provider, options Options) error {
	switch options.Engine {
	case "", EngineTerraform, EngineTofu:
	default:
		return fmt.Errorf("unknown --engine %q: use %s or %s", options.Engine, EngineTerraform, EngineTofu)
	}
	if err := checkImportOptions(options); err != nil {
		return err
	}
	if err := checkSelectionOptions(options); err != nil {
		return err
	}
	// An overlay can add services, such as an AWS cloud_control entry of
	// a new service, so the provider knows of them before the names are
	// checked.
	if options.Catalogue != "" {
		cat, err := providerCatalogue(provider, options.Catalogue)
		if err != nil {
			return fmt.Errorf("--catalogue: %w", err)
		}
		if p, ok := provider.(harvest.ProviderWithCatalogue); ok {
			p.SetCatalogue(cat)
		}
	}
	known := provider.Services()
	for _, f := range []struct {
		flag  string
		names []string
	}{{"--services", options.Services}, {"--skip", options.Skip}} {
		flag, names := f.flag, f.names
		var unknown []string
		for _, name := range names {
			if _, ok := known[name]; !ok && name != "*" {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			return fmt.Errorf("%s: %s has no service %s (infraharvest services %s lists them)", flag, provider.Name(), strings.Join(unknown, ", "), provider.Name())
		}
	}
	if err := checkLayout(options.Layout, options.EnvTags); err != nil {
		return err
	}
	if options.DefaultEnv != "" && len(options.EnvTags) == 0 && !strings.Contains(options.Layout, "{env}") {
		return errDefaultEnvWithoutSplit
	}
	for _, f := range []struct {
		flag     string
		negative bool
	}{
		{"--rate-limit", options.RateLimit < 0},
		{"--list-timeout", options.ListTimeout < 0},
		{"--list-parallel", options.ListParallel < 0},
		{"--parallel", options.Parallel < 0},
		{"--engine-timeout", options.EngineTimeout < 0},
	} {
		if f.negative {
			return fmt.Errorf("%s can't be negative", f.flag)
		}
	}
	return checkSources(options)
}

// errDefaultEnvWithoutSplit is the error for --default-env where no
// environment tag splits the roots.
var errDefaultEnvWithoutSplit = errors.New("--default-env names the environment of what no environment tag places: use it with --env-tags, or {env} in --layout")

// layoutPlaceholders are the placeholders a --layout may use.
var layoutPlaceholders = []string{"{out}", "{provider}", "{account}", "{region}", "{service}", "{env}"}

// placeholder matches a placeholder of a --layout.
var placeholder = regexp.MustCompile(`\{[^{}]*\}`)

// checkLayout rejects a --layout ("" is the default) that would put roots
// outside --out, or that has a placeholder nothing fills in, and
// --env-tags with a layout of its own that has no {env} to split
// environments into.
func checkLayout(layout string, envTags []string) error {
	if layout == "" {
		return nil
	}
	for _, p := range placeholder.FindAllString(layout, -1) {
		if !slices.Contains(layoutPlaceholders, p) {
			return fmt.Errorf("--layout %q: unknown placeholder %s: use %s", layout, p, strings.Join(layoutPlaceholders, ", "))
		}
	}
	rest, ok := strings.CutPrefix(layout, "{out}")
	if !ok || (rest != "" && rest[0] != '/' && rest[0] != '\\') {
		return fmt.Errorf("--layout %q must start with {out}/, so that the roots go under --out", layout)
	}
	segments := strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' })
	if slices.Contains(segments, "..") {
		return fmt.Errorf("--layout %q leads out of --out through a .. segment", layout)
	}
	if len(envTags) > 0 && layout != DefaultLayout && !strings.Contains(layout, "{env}") {
		return fmt.Errorf("--env-tags splits roots by environment, but --layout %q has no {env}: add it where the environment goes", layout)
	}
	return nil
}

// checkSources rejects the files and sources the import reads that can be
// checked before anything is listed: a --selection file that doesn't load
// (discover writes it instead), and a --managed-state source that doesn't
// parse or, for a local path, doesn't exist. The state itself is read
// once the resources are listed.
func checkSources(options Options) error {
	if options.Selection != "" && !options.Discover {
		if _, err := selection.Load(options.Selection); err != nil {
			return fmt.Errorf("--selection: %w", err)
		}
	}
	// The configured backend's state is resolved with the configuration
	// file (see excludeManaged).
	sources := make([]string, 0, len(options.ManagedState))
	for _, s := range options.ManagedState {
		if s != backendState {
			sources = append(sources, s)
		}
	}
	if err := managed.CheckSources(sources); err != nil {
		return fmt.Errorf("--managed-state: %w", err)
	}
	return nil
}

// ExitError is an error that sets the process's exit code (see the Exit
// codes of package report).
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	return e.Err
}

// ExitCode returns the exit code for err: report.ExitOK for nil, the code
// of an ExitError it wraps, and report.ExitCouldNotRun for any other
// error.
func ExitCode(err error) int {
	if err == nil {
		return report.ExitOK
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return report.ExitCouldNotRun
}

// checkFailures returns the error for an import in which failures, of
// services or resources, happened: nil if none did, else an error that
// wraps each of them and exits with report.ExitPartial when allowPartial
// accepts incomplete output, report.ExitIncomplete when it doesn't.
func checkFailures(failures []error, allowPartial bool) error {
	if len(failures) == 0 {
		return nil
	}
	err := fmt.Errorf("%d services or resources could not be imported:\n%w", len(failures), errors.Join(failures...))
	if allowPartial {
		return &ExitError{Code: report.ExitPartial, Err: fmt.Errorf("output is incomplete (--allow-partial is set): %w", err)}
	}
	return &ExitError{Code: report.ExitIncomplete, Err: fmt.Errorf("%w\noutput is incomplete; rerun with --allow-partial to accept partial output", err)}
}

// ResolveServices returns options with Services made concrete: "*"
// becomes every service of provider (see Services), and the services in
// Skip are taken out, keeping the order of the rest. options itself is
// left as it is.
func ResolveServices(provider harvest.Provider, options Options) Options {
	services := options.Services
	if slices.Contains(services, "*") {
		services = Services(provider)
		log.Printf("%s: --services=* stands for all %d services", provider.Name(), len(services))
	}
	kept := make([]string, 0, len(services))
	for _, service := range services {
		if slices.Contains(options.Skip, service) {
			log.Printf("%s: leaving out %s, which --skip names", provider.Name(), service)
			continue
		}
		kept = append(kept, service)
	}
	options.Services = kept
	return options
}

// listServices lists the resources of every service in
// options.Services into registry, each service with a provider of
// its own initialised with args. A service that fails to list, or that
// takes longer than options.ListTimeout, is left out of the registry and
// returned among the failures; one that lists in part (see
// harvest.Partial) keeps what it listed, and is among the failures too.
// The error is for what stops the whole listing: a provider that can't be
// initialised, a filter that doesn't parse, or an interrupt.
func listServices(ctx context.Context, registry *harvest.Registry, options Options, args []string) ([]error, error) {
	filters, err := options.filters()
	if err != nil {
		return nil, err
	}
	discoverers := make([]discovery.Discoverer, 0, len(options.Services))
	for _, service := range options.Services {
		serviceProvider := registry.Register(service)
		if err := serviceProvider.Init(ctx, args); err != nil {
			return nil, err
		}
		// The service's provider is new, so it is given the catalogue the
		// import's provider has, before it makes the service's lister.
		if p, ok := serviceProvider.(harvest.ProviderWithCatalogue); ok && options.cat != nil {
			p.SetCatalogue(options.cat)
		}
		if p, ok := serviceProvider.(unwantedTypesSetter); ok && len(options.unwanted) > 0 {
			p.SetUnwantedTypes(options.unwanted)
		}
		discoverers = append(discoverers, discovery.Service(service, serviceProvider, options.Verbose, filters))
	}

	results, err := discovery.List(ctx, discoverers, discovery.Options{Timeout: options.ListTimeout, Parallel: options.ListParallel})
	if err != nil {
		return nil, fmt.Errorf("listing: %w", err)
	}

	var failures []error
	var failed []string
	for _, result := range results {
		if result.Err == nil {
			continue
		}
		cause := result.Err
		if errors.Is(cause, discovery.ErrDeadline) {
			// Name the flag that sets the deadline, so the fix is plain.
			cause = fmt.Errorf("listing took longer than --list-timeout %s; its resources may be incomplete", options.ListTimeout)
		}
		if !harvest.IsPartial(cause) {
			failed = append(failed, result.Name)
		}
		failures = append(failures, fmt.Errorf("service %s: %w", result.Name, cause))
	}
	registry.Unregister(failed)
	registry.Collect()
	return failures, nil
}

// Services returns the names of provider's services, sorted, without its
// opt-in services (see harvest.ProviderWithOptInServices): those are
// imported only when named, never as part of "*".
func Services(provider harvest.Provider) []string {
	var optIn []string
	if p, ok := provider.(harvest.ProviderWithOptInServices); ok {
		optIn = p.OptInServices()
	}
	services := make([]string, 0, len(provider.Services()))
	for name := range provider.Services() {
		if !slices.Contains(optIn, name) {
			services = append(services, name)
		}
	}
	slices.Sort(services)
	return services
}

// filters returns the filters of --id, --tag and --where, in that order,
// or the error of the first that doesn't parse.
func (o Options) filters() ([]harvest.Filter, error) {
	var filters []harvest.Filter
	for _, flag := range []struct {
		values []string
		parse  func(string) (harvest.Filter, error)
	}{
		{o.IDFilters, harvest.ParseIDFilter},
		{o.TagFilters, harvest.ParseTagFilter},
		{o.WhereFilters, harvest.ParseWhereFilter},
	} {
		for _, raw := range flag.values {
			f, err := flag.parse(raw)
			if err != nil {
				return nil, err
			}
			filters = append(filters, f)
		}
	}
	return filters, nil
}

// filterArgs returns the filters as the command line gives them, such as
// --id=vpc=vpc-1: what an inventory records it was listed with.
func (o Options) filterArgs() []string {
	var args []string
	for _, flag := range []struct {
		name   string
		values []string
	}{{"id", o.IDFilters}, {"tag", o.TagFilters}, {"where", o.WhereFilters}} {
		for _, v := range flag.values {
			args = append(args, "--"+flag.name+"="+v)
		}
	}
	return args
}
