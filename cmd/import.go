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
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/terraformutils/terraformerstring"

	"github.com/IgnatG/infraharvest/terraformutils/providerwrapper"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/IgnatG/infraharvest/terraformutils/terraformoutput"

	"github.com/spf13/cobra"
)

type ImportOptions struct {
	Resources     []string
	Excludes      []string
	PathPattern   string
	PathOutput    string
	State         string
	Bucket        string
	Profile       string
	Verbose       bool
	Zone          string
	Regions       []string
	Projects      []string
	ResourceGroup string
	Connect       bool
	Compact       bool
	Filter        []string
	Plan          bool `json:"-"`
	Output        string
	NoSort        bool
	RetryCount    int
	RetrySleepMs  int
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
	// Modules says which modules hold clusters of resources: registry,
	// local or none.
	Modules string
}

const DefaultPathPattern = "{output}/{provider}/{service}/"
const DefaultPathOutput = "generated"
const DefaultState = "local"

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
		//Version:       version.String(),
	}

	cmd.PersistentFlags().String("config", "", "--engine=terraform or tofu: configuration file, which sets flags not given on the command line and the state backend of the generated roots")
	cmd.AddCommand(newCmdPlanImporter(options))
	cmd.AddCommand(&cobra.Command{
		Use:   "no-sort",
		Short: "Don't sort resources",
		Long:  "Don't sort resources",
	})
	for _, subcommand := range providerImporterSubcommands() {
		providerCommand := subcommand(options)
		_ = providerCommand.MarkPersistentFlagRequired("resources")
		if providerCommand.RunE != nil {
			providerCommand.RunE = withEngineRun(providerCommand.RunE)
		}
		cmd.AddCommand(providerCommand)
	}
	return cmd
}

func Import(provider terraformutils.ProviderGenerator, options ImportOptions, args []string) error {
	if options.Discover {
		return importWithEngine(provider, options, args)
	}
	switch options.Engine {
	case engineLegacy, "":
		if options.Selection != "" || options.All || len(options.ManagedState) > 0 {
			return errors.New("--selection, --all and --managed-state need --engine=terraform or tofu")
		}
	case engineTerraform, engineTofu:
		return importWithEngine(provider, options, args)
	default:
		return fmt.Errorf("unknown --engine %q: use %s, %s or %s", options.Engine, engineLegacy, engineTerraform, engineTofu)
	}

	providerWrapper, options, err := initOptionsAndWrapper(provider, options, args)
	if err != nil {
		return err
	}
	defer providerWrapper.Kill()
	providerMapping := terraformutils.NewProvidersMapping(provider)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	failures, err := initAllServicesResources(ctx, providerMapping, options, args, providerWrapper)
	if err != nil {
		return err
	}

	failures = append(failures, terraformutils.RefreshResourcesByProvider(providerMapping, providerWrapper)...)
	failures = append(failures, providerMapping.ConvertTFStates(providerWrapper)...)
	// change structs with additional data for each resource
	failures = append(failures, providerMapping.CleanupProviders()...)

	if err := importFromPlan(providerMapping, options, args); err != nil {
		return err
	}
	return checkFailures(failures, options.AllowPartial)
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

func initOptionsAndWrapper(provider terraformutils.ProviderGenerator, options ImportOptions, args []string) (*providerwrapper.ProviderWrapper, ImportOptions, error) {
	err := provider.Init(args)
	if err != nil {
		return nil, options, err
	}
	options = resolveServices(provider, options)

	providerWrapper, err := providerwrapper.NewProviderWrapper(provider.GetName(), provider.GetConfig(), options.Verbose, map[string]int{"retryCount": options.RetryCount, "retrySleepMs": options.RetrySleepMs})
	if err != nil {
		return nil, options, err
	}

	return providerWrapper, options, nil
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
func initAllServicesResources(ctx context.Context, providersMapping *terraformutils.ProvidersMapping, options ImportOptions, args []string, providerWrapper *providerwrapper.ProviderWrapper) (failures []error, err error) {
	var failedServices []string

	for _, service := range options.Resources {
		serviceProvider := providersMapping.AddServiceToProvider(service)
		if err := serviceProvider.Init(args); err != nil {
			return nil, err
		}
		err := initServiceResources(ctx, service, serviceProvider, options, providerWrapper)
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
	providersMapping.ProcessResources(false)

	return failures, nil
}

func importFromPlan(providerMapping *terraformutils.ProvidersMapping, options ImportOptions, args []string) error {
	plan := &ImportPlan{
		Provider:         providerMapping.GetBaseProvider().GetName(),
		Options:          options,
		Args:             args,
		ImportedResource: map[string][]terraformutils.Resource{},
	}

	resourcesByService := providerMapping.GetResourcesByService()
	for service := range resourcesByService {
		plan.ImportedResource[service] = append(plan.ImportedResource[service], resourcesByService[service]...)
	}

	if options.Plan {
		path := Path(options.PathPattern, providerMapping.GetBaseProvider().GetName(), "infraharvest", options.PathOutput)
		return ExportPlanFile(plan, path, "plan.json")
	}

	return ImportFromPlan(providerMapping.GetBaseProvider(), plan)
}

// initServiceResources lists one service's resources, with ctx and at most
// options.ListTimeout for the service's API calls.
func initServiceResources(ctx context.Context, service string, provider terraformutils.ProviderGenerator,
	options ImportOptions, providerWrapper *providerwrapper.ProviderWrapper) error {
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

	// Ignore keys come from the provider schema, which only the legacy engine
	// loads; the Terraform engine runs without a provider wrapper.
	if providerWrapper != nil {
		provider.GetService().PopulateIgnoreKeys(providerWrapper)
	}
	provider.GetService().InitialCleanup()
	log.Println(provider.GetName() + " done importing " + service)

	return nil
}

func ImportFromPlan(provider terraformutils.ProviderGenerator, plan *ImportPlan) error {
	options := plan.Options
	importedResource := plan.ImportedResource
	isServicePath := strings.Contains(options.PathPattern, "{service}")

	if options.Connect {
		log.Println(provider.GetName() + " Connecting.... ")
		importedResource = terraformutils.ConnectServices(importedResource, isServicePath, provider.GetResourceConnections())
	}

	if !isServicePath {
		var compactedResources []terraformutils.Resource
		for _, resources := range importedResource {
			compactedResources = append(compactedResources, resources...)
		}
		e := printService(provider, "", options, compactedResources, importedResource)
		if e != nil {
			return e
		}
	} else {
		for serviceName, resources := range importedResource {
			e := printService(provider, serviceName, options, resources, importedResource)
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func printService(provider terraformutils.ProviderGenerator, serviceName string, options ImportOptions, resources []terraformutils.Resource, importedResource map[string][]terraformutils.Resource) error {
	log.Println(provider.GetName() + " save " + serviceName)
	// Print HCL files for Resources
	path := Path(options.PathPattern, provider.GetName(), serviceName, options.PathOutput)
	err := terraformoutput.OutputHclFiles(resources, provider, path, serviceName, options.Compact, options.Output, !options.NoSort)
	if err != nil {
		return err
	}
	tfStateFile, err := terraformutils.PrintTfState(resources)
	if err != nil {
		return err
	}
	// print or upload State file
	if options.State == "bucket" {
		log.Println(provider.GetName() + " upload tfstate to  bucket " + options.Bucket)
		bucket := terraformoutput.BucketState{
			Name: options.Bucket,
		}
		if err := bucket.BucketUpload(path, tfStateFile); err != nil {
			return err
		}
		// create Bucket file
		if bucketStateDataFile, err := terraformutils.Print(bucket.BucketGetTfData(path), map[string]struct{}{}, options.Output, !options.NoSort); err == nil {
			terraformoutput.PrintFile(path+"/bucket.tf", bucketStateDataFile)
		}
	} else {
		if serviceName == "" {
			log.Println(provider.GetName() + " save tfstate")
		} else {
			log.Println(provider.GetName() + " save tfstate for " + serviceName)
		}
		if err := terraformutils.WriteSecretFile(path+"/terraform.tfstate", tfStateFile); err != nil {
			return err
		}
	}
	// Print hcl variables.tf
	if serviceName != "" {
		if options.Connect && len(provider.GetResourceConnections()[serviceName]) > 0 {
			variables := map[string]map[string]map[string]interface{}{}
			variables["data"] = map[string]map[string]interface{}{}
			variables["data"]["terraform_remote_state"] = map[string]interface{}{}
			if options.State == "bucket" {
				bucket := terraformoutput.BucketState{
					Name: options.Bucket,
				}
				for k := range provider.GetResourceConnections()[serviceName] {
					if _, exist := importedResource[k]; !exist {
						continue
					}
					variables["data"]["terraform_remote_state"][k] = map[string]interface{}{
						"backend": "gcs",
						"config":  bucket.BucketGetTfData(Path(options.PathPattern, provider.GetName(), k, options.PathOutput)),
					}
				}
			} else {
				for k := range provider.GetResourceConnections()[serviceName] {
					if _, exist := importedResource[k]; !exist {
						continue
					}
					statePath, err := relativeStatePath(path, Path(options.PathPattern, provider.GetName(), k, options.PathOutput))
					if err != nil {
						return err
					}
					variables["data"]["terraform_remote_state"][k] = map[string]interface{}{
						"backend": "local",
						"config": map[string]interface{}{
							"path": statePath,
						},
					}
				}
			}
			// create variables file
			if len(provider.GetResourceConnections()[serviceName]) > 0 && options.Connect && len(variables["data"]["terraform_remote_state"]) > 0 {
				variablesFile, err := terraformutils.Print(variables, map[string]struct{}{"config": {}}, options.Output, !options.NoSort)
				if err != nil {
					return err
				}
				terraformoutput.PrintFile(path+"/variables."+terraformoutput.GetFileExtension(options.Output), variablesFile)
			}
		}
	} else {
		if options.Connect {
			variables := map[string]map[string]map[string]interface{}{}
			variables["data"] = map[string]map[string]interface{}{}
			variables["data"]["terraform_remote_state"] = map[string]interface{}{}
			if options.State == "bucket" {
				bucket := terraformoutput.BucketState{
					Name: options.Bucket,
				}
				variables["data"]["terraform_remote_state"]["local"] = map[string]interface{}{
					"backend": "gcs",
					"config":  bucket.BucketGetTfData(path),
				}
			} else {
				variables["data"]["terraform_remote_state"]["local"] = map[string]interface{}{
					"backend": "local",
					"config": map[string]interface{}{
						"path": "terraform.tfstate",
					},
				}
			}
			// create variables file
			if options.Connect {
				variablesFile, err := terraformutils.Print(variables, map[string]struct{}{"config": {}}, options.Output, !options.NoSort)
				if err != nil {
					return err
				}
				terraformoutput.PrintFile(path+"/variables."+terraformoutput.GetFileExtension(options.Output), variablesFile)
			}
		}
	}
	return nil
}

// relativeStatePath returns the path of the local state file in stateDir,
// relative to fromDir, using the forward slashes Terraform expects.
func relativeStatePath(fromDir, stateDir string) (string, error) {
	rel, err := filepath.Rel(fromDir, filepath.Join(stateDir, "terraform.tfstate"))
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
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
		RunE: func(cmd *cobra.Command, args []string) error {
			services := providerServices(provider)
			for _, k := range services {
				fmt.Println(k)
			}
			return nil
		},
	}
	cmd.Flags().AddFlag(&pflag.Flag{Name: "resources"})
	return cmd
}

func providerServices(provider terraformutils.ProviderGenerator) []string {
	var services []string
	for k := range provider.GetSupportedService() {
		services = append(services, k)
	}
	sort.Strings(services)
	return services
}

func baseProviderFlags(flag *pflag.FlagSet, options *ImportOptions, sampleRes, sampleFilters string) {
	flag.BoolVarP(&options.Connect, "connect", "c", true, "")
	flag.BoolVarP(&options.Compact, "compact", "C", false, "")
	flag.StringSliceVarP(&options.Resources, "resources", "r", []string{}, sampleRes)
	flag.StringSliceVarP(&options.Excludes, "excludes", "x", []string{}, sampleRes)
	flag.StringVarP(&options.PathPattern, "path-pattern", "p", DefaultPathPattern, "{output}/{provider}/")
	flag.StringVarP(&options.PathOutput, "path-output", "o", DefaultPathOutput, "")
	flag.StringVarP(&options.State, "state", "s", DefaultState, "local or bucket")
	flag.StringVarP(&options.Bucket, "bucket", "b", "", "gs://terraform-state")
	flag.StringSliceVarP(&options.Filter, "filter", "f", []string{}, sampleFilters)
	flag.BoolVarP(&options.Verbose, "verbose", "v", false, "")
	flag.BoolVarP(&options.NoSort, "no-sort", "S", false, "set to disable sorting of HCL")
	flag.StringVarP(&options.Output, "output", "O", outputHCL, "hcl or json. Legacy engine: format of the generated files. Terraform or OpenTofu engine: json prints the import report as JSON on stdout")
	flag.IntVarP(&options.RetryCount, "retry-number", "n", 5, "number of retries to perform when refresh fails")
	flag.IntVarP(&options.RetrySleepMs, "retry-sleep-ms", "m", 300, "time in ms to sleep between retries")
	flag.DurationVar(&options.ListTimeout, "list-timeout", DefaultListTimeout, "longest time to list one service in one region; a service that takes longer is reported as failed (0: no limit)")
	flag.BoolVar(&options.AllowPartial, "allow-partial", false, "keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1")
	flag.StringVar(&options.Selection, "selection", "", "--engine=terraform or tofu: selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml)")
	flag.StringSliceVar(&options.ManagedState, "managed-state", nil, "--engine=terraform or tofu: leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...] (all its .tfstate objects); backend reads the configured S3 backend's state")
	flag.BoolVar(&options.All, "all", false, "--engine=terraform or tofu: import everything the default selection includes, without a selection file")
	flag.StringVar(&options.Modules, "modules", modulesRegistry, "--engine=terraform or tofu: registry moves clusters of resources into curated public modules (terraform-aws-modules) where the plan stays the same, else into generated local modules; local uses generated local modules only; none keeps every resource in the root")
	flag.StringVar(&options.Engine, "engine", engineLegacy, "legacy, terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks (no state written)")
	flag.StringVar(&options.TerraformPath, "terraform-path", "", "Terraform or OpenTofu binary for --engine=terraform or tofu (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6)")
}
