// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/terraformutils/terraformerstring"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/terraformutils"

	"github.com/spf13/cobra"
)

type ImportOptions struct {
	Resources     []string
	Excludes      []string
	PathPattern   string
	PathOutput    string
	Profile       string
	Verbose       bool
	Zone          string
	Regions       []string
	Projects      []string
	ResourceGroup string
	Filter        []string
	// Output is hcl, or json to also print the import report as JSON on
	// stdout.
	Output string
	// ListTimeout limits listing one service in one region; 0 means no
	// limit.
	ListTimeout   time.Duration
	AllowPartial  bool
	Engine        string
	TerraformPath string
	// Selection is the selection file to import from, or, for discover,
	// the one to write.
	Selection string
	// All imports everything the default selection includes, without a
	// selection file.
	All bool
	// Discover writes a selection file instead of importing.
	Discover bool `json:"-"`
	// ManagedState is state to read, so that an import leaves out what
	// Terraform already manages (see excludeManaged).
	ManagedState []string
	// Resume skips the roots a previous run generated from the same imports
	// and options (see resumed).
	Resume bool
	// Incremental adds the resources a root doesn't have yet to the roots
	// earlier imports generated (see addToRoot).
	Incremental bool
	// ReuseInventory imports from the resources discover listed, if it
	// listed the same services (see listResources).
	ReuseInventory bool
	// Accounts, Organization and AssumeRole import several AWS accounts,
	// each through a role (see awsAccounts); RoleARN is the role of the
	// account being imported.
	Accounts     []string
	Organization bool
	AssumeRole   string
	RoleARN      string `json:"-"`
	// Modules says which modules hold clusters of resources: registry,
	// local or none.
	Modules string
	// Parallel is how many of the accounts (AWS) or projects (Google) the
	// import covers are imported at once (see importEach).
	Parallel int
	// run is the run an account or project imported alongside others
	// records into (see importEach); nil for the command's run.
	run *engineRun
}

const DefaultPathOutput = "generated"

// DefaultListTimeout is the default of --list-timeout.
const DefaultListTimeout = 30 * time.Minute

func newImportCmd() *cobra.Command {
	options := ImportOptions{}
	cmd := &cobra.Command{
		Use:           "import",
		Short:         "Import current state to Terraform configuration",
		Long:          "Import current state to Terraform configuration",
		SilenceUsage:  true,
		SilenceErrors: false,
	}

	cmd.PersistentFlags().String("config", "", "configuration file, which sets flags not given on the command line and the state backend of the generated roots")
	for _, subcommand := range providerImporterSubcommands() {
		providerCommand := subcommand(options)
		if providerCommand.RunE != nil {
			providerCommand.RunE = withEngineRun(providerCommand.RunE)
		}
		cmd.AddCommand(providerCommand)
	}
	return cmd
}

// Import lists the resources of options.Resources with the provider's
// listers, then lets Terraform or OpenTofu (--engine) generate their
// configuration from import blocks; for discover, it lists them into a
// selection file instead.
func Import(provider terraformutils.ProviderGenerator, options ImportOptions, args []string) error {
	switch options.Engine {
	case "", engineTerraform, engineTofu:
	default:
		return fmt.Errorf("unknown --engine %q: use %s or %s", options.Engine, engineTerraform, engineTofu)
	}
	return importWithEngine(provider, options, args)
}

// checkFailures reports services and resources that could not be imported.
// The output written so far is incomplete: the error exits with
// report.ExitPartial if the user accepted that with --allow-partial, else
// with report.ExitIncomplete.
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

// ExitError ends the program with Code. See package report for the codes.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }

func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode returns the code the program exits with after err: 0 for nil,
// an ExitError's code, or report.ExitCouldNotRun for any other error.
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

// resolveServices expands "*" to every supported service and drops excluded
// services.
func resolveServices(provider terraformutils.ProviderGenerator, options ImportOptions) ImportOptions {
	if terraformerstring.ContainsString(options.Resources, "*") {
		log.Println("Attempting an import of ALL resources in " + provider.GetName())
		options.Resources = providerServices(provider)
	}

	if len(options.Excludes) > 0 {
		localSlice := []string{}
		for _, r := range options.Resources {
			remove := false
			for _, e := range options.Excludes {
				if r == e {
					remove = true
					log.Println("Excluding resource " + e)
				}
			}
			if !remove {
				localSlice = append(localSlice, r)
			}
		}
		options.Resources = localSlice
	}
	return options
}

// listContext returns ctx with timeout, if there is one.
func listContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// initAllServicesResources lists the resources of every requested service.
// A service that fails is left out and reported in failures; err is set
// only when the provider itself cannot be initialised, or ctx is done
// (the user interrupted).
func initAllServicesResources(ctx context.Context, providersMapping *terraformutils.ProvidersMapping, options ImportOptions, args []string) (failures []error, err error) {
	var failedServices []string

	for _, service := range options.Resources {
		serviceProvider := providersMapping.AddServiceToProvider(service)
		if err := serviceProvider.Init(args); err != nil {
			return nil, err
		}
		err := initServiceResources(ctx, service, serviceProvider, options)
		if ctx.Err() != nil {
			return nil, fmt.Errorf("listing %s: %w", service, ctx.Err())
		}
		if err != nil {
			failedServices = append(failedServices, service)
			failures = append(failures, fmt.Errorf("service %s: %w", service, err))
		}
	}

	// remove providers that failed to init their service
	providersMapping.RemoveServices(failedServices)
	providersMapping.ProcessResources()

	return failures, nil
}

// initServiceResources lists one service's resources, with ctx and at most
// options.ListTimeout for the service's API calls.
func initServiceResources(ctx context.Context, service string, provider terraformutils.ProviderGenerator, options ImportOptions) error {
	log.Println(provider.GetName() + " importing... " + service)
	err := provider.InitService(service, options.Verbose)
	if err != nil {
		log.Printf("%s error importing %s, err: %s\n", provider.GetName(), service, err)
		return err
	}
	provider.GetService().ParseFilters(options.Filter)
	listCtx, cancel := listContext(ctx, options.ListTimeout)
	defer cancel()
	provider.GetService().SetContext(listCtx)
	err = provider.GetService().InitResources()
	// Many listers log a failed call and go on: past the deadline, what
	// they found is incomplete even without an error.
	if ctx.Err() == nil && listCtx.Err() != nil {
		err = fmt.Errorf("listing took longer than --list-timeout %s; its resources may be incomplete", options.ListTimeout)
	}
	if err != nil {
		log.Printf("%s error initializing resources in service %s, err: %s\n", provider.GetName(), service, err)
		return err
	}

	provider.GetService().InitialCleanup()
	log.Println(provider.GetName() + " done importing " + service)

	return nil
}

func Path(pathPattern, providerName, serviceName, output string) string {
	return strings.NewReplacer(
		"{provider}", providerName,
		"{service}", serviceName,
		"{output}", output,
	).Replace(pathPattern)
}

func listCmd(provider terraformutils.ProviderGenerator) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List supported resources for " + provider.GetName() + " provider",
		Long:  "List supported resources for " + provider.GetName() + " provider",
		RunE: func(_ *cobra.Command, _ []string) error {
			services := providerServices(provider)
			for _, k := range services {
				fmt.Println(k)
			}
			return nil
		},
	}
	return cmd
}

func providerServices(provider terraformutils.ProviderGenerator) []string {
	var optIn []string
	if p, ok := provider.(terraformutils.ProviderWithOptInServices); ok {
		optIn = p.OptInServices()
	}
	var services []string
	for k := range provider.GetSupportedService() {
		if !slices.Contains(optIn, k) {
			services = append(services, k)
		}
	}
	sort.Strings(services)
	return services
}

func baseProviderFlags(flag *pflag.FlagSet, options *ImportOptions, sampleRes, sampleFilters string) {
	flag.StringSliceVarP(&options.Resources, "resources", "r", []string{}, sampleRes)
	flag.StringSliceVarP(&options.Excludes, "excludes", "x", []string{}, sampleRes)
	flag.StringVarP(&options.PathPattern, "path-pattern", "p", DefaultRootPathPattern, "layout of the output directories, one root each, from {output}, {provider}, {account}, {region} and {service}")
	flag.StringVarP(&options.PathOutput, "path-output", "o", DefaultPathOutput, "")
	flag.StringSliceVarP(&options.Filter, "filter", "f", []string{}, sampleFilters)
	flag.BoolVarP(&options.Verbose, "verbose", "v", false, "")
	flag.StringVarP(&options.Output, "output", "O", outputHCL, "hcl, or json to also print the import report as JSON on stdout")
	flag.DurationVar(&options.ListTimeout, "list-timeout", DefaultListTimeout, "longest time to list one service in one region; a service that takes longer is reported as failed (0: no limit)")
	flag.BoolVar(&options.AllowPartial, "allow-partial", false, "keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1")
	flag.StringVar(&options.Selection, "selection", "", "selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml)")
	flag.StringSliceVar(&options.ManagedState, "managed-state", nil, "leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...&profile=...] (read with --profile unless it names one) gs://bucket/prefix or https://<account>.blob.core.windows.net/<container>/prefix (all their .tfstate objects), or tfc://<organization>/<workspace> (<prefix>* for several, ?host= for Terraform Enterprise); backend reads the configured S3, GCS or azurerm backend's state")
	flag.BoolVar(&options.Resume, "resume", false, "skip the roots a previous run generated from the same resources and options, such as after a run that failed part way")
	flag.BoolVar(&options.Incremental, "incremental", false, "add what is new to the roots earlier imports generated in --path-output, in a file of its own, without changing what they have")
	flag.BoolVar(&options.ReuseInventory, "reuse-inventory", false, "import from the resources infraharvest discover listed into the same --path-output, instead of listing them again")
	flag.BoolVar(&options.All, "all", false, "import everything the default selection includes, without a selection file")
	flag.StringVar(&options.Modules, "modules", modulesRegistry, "registry moves clusters of resources into curated public modules (terraform-aws-modules), at the release each adapter is tested with, where the plan stays the same, else into generated local modules; latest-untested calls each module's newest release instead; local uses generated local modules only; none keeps every resource in the root")
	flag.StringVar(&options.Engine, "engine", engineTerraform, "terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks")
	flag.StringVar(&options.TerraformPath, "terraform-path", "", "Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6)")
}
