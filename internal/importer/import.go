// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/IgnatG/infraharvest/internal/discovery"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// Defaults of the flags every provider command shares.
const (
	// DefaultOut is the directory generated roots go into.
	DefaultOut = "infraharvest"
	// DefaultListTimeout is how long one service may list in one scope.
	DefaultListTimeout = 30 * time.Minute
	// DefaultListParallel is how many services of one scope list at once.
	DefaultListParallel = 4
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
	// EnvTags are the tag keys whose value names a resource's environment.
	EnvTags []string
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
	// AllowPartial accepts output some services or resources are missing
	// from.
	AllowPartial bool
	// Resume skips the roots a previous, identical run generated.
	Resume bool
	// Incremental adds what is new to roots earlier runs generated.
	Incremental bool
	// ReuseInventory imports from what discover listed, instead of listing
	// again.
	ReuseInventory bool

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
	// once (see ImportEach).
	Parallel int

	// run is the run this call records into, when it isn't the command's
	// own: one of several scopes imported at once has a run of its own
	// (see ImportEach).
	run *Run
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
// returned among the failures; the error is for what stops the whole
// listing: a provider that can't be initialised, a filter that doesn't
// parse, or an interrupt.
func listServices(ctx context.Context, registry *harvest.Registry, options Options, args []string) ([]error, error) {
	filters, err := options.filters()
	if err != nil {
		return nil, err
	}
	discoverers := make([]discovery.Discoverer, 0, len(options.Services))
	for _, service := range options.Services {
		serviceProvider := registry.Register(service)
		if err := serviceProvider.Init(args); err != nil {
			return nil, err
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
		failed = append(failed, result.Name)
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
