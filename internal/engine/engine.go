// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package engine generates Terraform configuration for existing resources by
// running Terraform itself. It writes `import` blocks and lets
// `terraform plan -generate-config-out` produce the resource configuration.
// It never writes state and never applies.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// Files the engine writes into each output directory.
const (
	VersionsFileName  = "versions.tf"
	ProvidersFileName = "providers.tf"
	ImportsFileName   = "imports.tf"
	GeneratedFileName = "generated.tf"
	VariablesFileName = "variables.tf"
	// RejectedFileName holds resources left out of the configuration. It is
	// not a .tf file, so Terraform doesn't load it.
	RejectedFileName = "rejected.hcl"
	// LockFileName is Terraform's dependency lock file.
	LockFileName = ".terraform.lock.hcl"
)

// maxPlanRounds bounds how often Generate plans again after repairing the
// configuration or leaving resources out.
const maxPlanRounds = 3

// Terraform is the subset of *tfexec.Terraform the engine uses.
type Terraform interface {
	Init(ctx context.Context, opts ...tfexec.InitOption) error
	PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error)
	Validate(ctx context.Context) (*tfjson.ValidateOutput, error)
	ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error)
	ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error)
	FormatCheck(ctx context.Context, opts ...tfexec.FormatOption) (bool, []string, error)
	SetStdout(w io.Writer)
}

// NewTerraform returns a Terraform runner for dir that shares downloaded
// providers through pluginCacheDir, with env on top of the process's
// environment, such as the credentials the provider resolved. It creates
// both directories. Its init holds the plugin cache's lock (see
// Runner.Init).
func NewTerraform(dir, execPath, pluginCacheDir string, env map[string]string) (*Runner, error) {
	for _, d := range []string{dir, pluginCacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		return nil, err
	}
	if err := tf.SetEnv(terraformEnviron(pluginCacheDir, env)); err != nil {
		return nil, err
	}
	return &Runner{Terraform: tf, pluginCacheDir: pluginCacheDir}, nil
}

// Runner is the Terraform runner NewTerraform returns: tfexec's, whose
// init holds the lock of the plugin cache it shares.
type Runner struct {
	*tfexec.Terraform
	pluginCacheDir string
}

// Init runs terraform init holding the plugin cache's lock (see
// lockPluginCache): other processes, such as another infraharvest run on
// the machine, share the cache, and Terraform doesn't guarantee that
// installs into it at the same time are safe.
func (r *Runner) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	lock, err := lockPluginCache(ctx, r.pluginCacheDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	return r.Terraform.Init(ctx, opts...)
}

// terraformEnviron is the environment Terraform runs with: the process's,
// with env on top, and the plugin cache pluginCacheDir. SetEnv replaces
// the inherited environment and rejects variables that tfexec manages
// itself (TF_LOG, TF_CLI_ARGS, ...), so those are dropped.
func terraformEnviron(pluginCacheDir string, env map[string]string) map[string]string {
	all := tfexec.CleanEnv(mergeEnv(environ(), env, runtime.GOOS == "windows"))
	all["TF_PLUGIN_CACHE_DIR"] = pluginCacheDir
	return all
}

// NewFreshEnvTerraform returns a Terraform runner for dir like
// NewTerraform, with env on top of the process's environment, which it
// asks fresh for again before each plan: plans are the commands that call
// the cloud, and a root may plan for longer than temporary credentials
// last, such as an SSO session's, so each plan runs with credentials as
// fresh as the first. An error of fresh fails that plan.
func NewFreshEnvTerraform(dir, execPath, pluginCacheDir string, env map[string]string, fresh func(context.Context) (map[string]string, error)) (Terraform, error) {
	tf, err := NewTerraform(dir, execPath, pluginCacheDir, env)
	if err != nil {
		return nil, err
	}
	return &freshEnvTerraform{Runner: tf, pluginCacheDir: pluginCacheDir, env: fresh}, nil
}

// freshEnvTerraform is a Terraform runner that sets its environment again,
// from env, before each plan (see NewFreshEnvTerraform). The engine runs a
// root's commands one at a time, so the environment never changes under a
// running one.
type freshEnvTerraform struct {
	*Runner
	pluginCacheDir string
	env            func(context.Context) (map[string]string, error)
}

// PlanJSON plans with the environment env gives now.
func (t *freshEnvTerraform) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	env, err := t.env(ctx)
	if err != nil {
		return false, err
	}
	if err := t.SetEnv(terraformEnviron(t.pluginCacheDir, env)); err != nil {
		return false, err
	}
	return t.Runner.PlanJSON(ctx, w, opts...)
}

// Result is what Generate leaves for the user to finish.
type Result struct {
	// Imported are the resources the configuration imports, with the
	// labels Generate gave them.
	Imported []Import
	// Secrets are the variables to set before planning.
	Secrets []Secret
	// Rejected are the resources left out of the configuration.
	Rejected []Rejection
	// Owned are the resources left out because their tags say another
	// tool manages them (see OwnedBy).
	Owned []OwnedResource `json:",omitempty"`
	// Gate is the result of the verification gate (see runGate).
	Gate Gate
	// Warnings are about what the configuration holds that people should
	// look at, such as a tag named after an AWS access key ID.
	Warnings []string `json:",omitempty"`
	// Modules are the module calls the configuration makes, and the
	// clusters of resources Generate didn't move into one, with why.
	Modules []ModuleCall
	// Files are the files the configuration is in besides generated.tf,
	// with Options.SplitFiles.
	Files []string `json:",omitempty"`
	// Addresses are where the root's import blocks import each resource,
	// as Generate finally wrote them, module calls included, so that a
	// later run can tell which resources it would give other addresses.
	Addresses []Managed `json:",omitempty"`
	// Moves are the moved blocks of moves.tf, for a root generated again
	// whose resources have other addresses than before (see
	// Options.Previous).
	Moves []Move `json:",omitempty"`
	// Removed are the removed blocks of moves.tf, for a root generated
	// again that hands resources over to other roots (see
	// Previous.Removed), and Arrived the resources it imports that other
	// roots hand over (see Options.Arrivals).
	Removed []Removal `json:",omitempty"`
	Arrived []Arrival `json:",omitempty"`
	// Retyped are the resources of such a root that are of another type now,
	// another name of the one they were, which its removed blocks forget
	// at the addresses they had and its import blocks import again (see
	// Retype).
	Retyped []Retype `json:",omitempty"`
	// Unmoved are the resources of such a root at other addresses that no
	// moved block can follow, with why; a checkpoint doesn't keep them.
	Unmoved []string `json:"-"`
	// NotInCloud are the resources of the root an incremental import added
	// to that its listing found no longer in the cloud, for the README; a
	// checkpoint doesn't keep them, as the next run looks again.
	NotInCloud []Import `json:"-"`
}

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
	ModuleProviders map[string]Provider
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
	// Scanners run in the verification gate (see runScanners).
	Scanners []Scanner
	// IncludeOwned keeps the resources whose tags say another tool
	// manages them, which Generate leaves out otherwise (see OwnedBy).
	IncludeOwned bool
	// KeepOwned are resources, as "<type> <import ID>", to keep although
	// their tags say another tool manages them, such as those a selection
	// file includes.
	KeepOwned map[string]bool
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
	Arrivals []Arrival
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
	// RefineKey identifies what Refine carries into the root, such as the
	// templates and parameter values of its stacks, for the fingerprint
	// --resume compares: a root whose Refine input changed is generated
	// again.
	RefineKey string `json:"-"`
}

// run is one run of the pipeline, Generate's, of a root of its own (see
// generating), or Add's, of resources to add to a root an earlier import
// generated (see adding): the Options it runs with, which for Add's
// differ from the caller's, and what it finds on the way, which the
// passes receive with it. The passes and the gate of the directory the
// run generates in read its Options; Add checks the root it adds to with
// the caller's own (see Add).
type run struct {
	opts Options
	// taken are names the run leaves alone, such as those of the root an
	// add adds to.
	taken Names
	// secrets gives the run's own plans the values of the secrets it found
	// in the configuration (see literalSecretsToVariables), once it found
	// any.
	secrets *secretValues
	// modules counts the users of the generated modules the run takes up
	// (see Options.Modules).
	modules *ModuleRefs
}

// generating is Generate's run with opts: a root of its own, with the
// settings opts gives.
func generating(opts Options) *run {
	modules := opts.Modules
	if modules == nil {
		modules = &ModuleRefs{}
	}
	return &run{opts: opts, modules: modules}
}

// replanReason names an edit of generated.tf that makes Generate plan it
// again before it reads the plan's errors: the edit moves the lines they
// point at, or resolves some of them (see prepareGenerated).
type replanReason string

const (
	replanSorted       replanReason = "resources sorted"
	replanOmitted      replanReason = "arguments left out"
	replanPlaceholders replanReason = "placeholders set"
	replanLifecycles   replanReason = "lifecycles set"
	replanConflicts    replanReason = "conflicting arguments settled"
	replanSecrets      replanReason = "secrets moved into variables"
)

// Generate writes opts.Config and an import block per resource into dir,
// then runs Terraform to generate the configuration of every imported
// resource into generated.tf, without the arguments opts.Omit names.
// Resource names become labels (see Label). If Terraform
// rejects what it generated, Generate repairs it (see repair) and plans
// again. Resources it still can't plan are left out (see Rejection). Secret
// values Terraform doesn't write into the configuration become sensitive
// variables in variables.tf (see Secret). Last, it writes a README with
// what is left to do.
//
// Generate fails if Terraform can't plan the directory at all. It refuses
// to overwrite an existing generated.tf or imports.tf.
func Generate(ctx context.Context, tf Terraform, dir string, imports []Import, opts Options) (*Result, error) {
	return generate(ctx, tf, dir, imports, generating(opts))
}

// generate is Generate's pipeline, run with r's options. It runs in
// stages, each a method of generation: it writes the root and has
// Terraform generate its configuration (write, planGenerated), moves the
// secrets that holds into variables (hideSecrets), plans until what it
// keeps is valid (planUntilValid), leaves out what the root doesn't
// import (leaveOut), makes the secret attributes read from variables
// (secretVariables), tidies the configuration (tidy, finish) and gates it
// (gate). Last, it writes the README.
func generate(ctx context.Context, tf Terraform, dir string, imports []Import, r *run) (*Result, error) {
	if len(imports) == 0 {
		return nil, errors.New("no resources to import")
	}
	generated := filepath.Join(dir, GeneratedFileName)
	// imports.tf too: with SplitFiles, an earlier run leaves no generated.tf.
	for _, path := range []string{generated, filepath.Join(dir, ImportsFileName)} {
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("%s already exists; use an empty output directory", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	// Several steps need the providers' schemas: read them once per init.
	// No plan shows the secret values Generate finds (see
	// redactingTerraform), and none reads the state the root may have
	// locally (see withoutLocalState).
	tf, removeEmptyState, err := withoutLocalState(tf, dir)
	if err != nil {
		return nil, err
	}
	defer removeEmptyState()
	redacting := &redactingTerraform{Terraform: withTimeout(tf, r.opts.CommandTimeout)}
	g := &generation{
		tf:        &schemaCache{Terraform: redacting},
		redacting: redacting,
		dir:       dir,
		generated: generated,
		imports:   relabelled(imports, r.taken, r.opts.TidyLabels, r.opts.Previous),
		r:         r,
		opts:      r.opts,
		result:    &Result{},
		leftOut:   map[string]bool{},
	}
	restoreAccess, err := g.write(ctx)
	if err != nil {
		return nil, err
	}
	reasons, err := g.planGenerated(ctx)
	if err != nil {
		return nil, err
	}
	// The values of the secrets hideSecrets moves, which the run's plans
	// read, go with the run.
	defer func() { r.secrets.remove() }()
	if err := g.hideSecrets(); err != nil {
		return nil, err
	}
	if len(g.literal) > 0 {
		reasons = append(reasons, replanSecrets)
	}
	if err := restoreAccess(); err != nil {
		return nil, err
	}
	if err := g.planUntilValid(ctx, reasons); err != nil {
		return nil, err
	}
	if err := g.leaveOut(); err != nil {
		return nil, err
	}
	if err := g.secretVariables(ctx); err != nil {
		return nil, err
	}
	if err := g.tidy(ctx); err != nil {
		return nil, err
	}
	if err := g.finish(ctx); err != nil {
		return nil, err
	}
	if err := g.gate(ctx); err != nil {
		return nil, err
	}
	// The README shows the gate's details, which come from plans that had
	// the secret values: the gate couldn't scan it, so it is scanned here.
	redactResult(g.result, g.literal)
	if err := WriteReadme(dir, g.result); err != nil {
		return g.result, err
	}
	return g.result, checkReadme(dir, g.result, knownValues(g.literal))
}

// generation is the state generate's stages share as they generate a
// root in dir.
type generation struct {
	// tf runs Terraform in dir, without showing the secret values hidden
	// with redacting.
	tf        Terraform
	redacting *redactingTerraform
	dir       string
	// generated is the path of generated.tf.
	generated string
	imports   []Import
	r         *run
	opts      Options
	// literal are the secrets found in the configuration Terraform
	// generated (see hideSecrets).
	literal []Secret
	// diags are the errors of the last plan.
	diags []tfjson.Diagnostic
	// last is the last of the plans planUntilValid makes, if it had no
	// errors, and planned the files it read: tidy starts from it if they
	// are still the same then.
	last    *shownPlan
	planned map[string][]byte
	result  *Result
	// rejected is the configuration of the resources left out, for
	// rejected.tf.
	rejected bytes.Buffer
	// leftOut are the addresses of the resources the root doesn't import.
	leftOut map[string]bool
}

// write writes the root's files and initialises it. It makes dir private
// first and returns what makes it public again, once the secrets are out
// of generated.tf.
func (g *generation) write(ctx context.Context) (restoreAccess func() error, err error) {
	opts, dir := g.opts, g.dir
	importsHCL, err := ImportsFile(g.imports)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Terraform writes every value the provider doesn't mark sensitive
	// into generated.tf, secrets among them, and with the mode it chooses.
	// The directory stays private until those secrets are moved out of
	// the file (see hideSecrets); should Generate fail before that, it
	// stays so. On Windows, where a mode restricts nothing, its access
	// list does it.
	if restoreAccess, err = makePrivate(dir); err != nil {
		return nil, err
	}
	files := map[string][]byte{ImportsFileName: importsHCL}
	for name, content := range opts.Config {
		files[name] = content
	}
	for name, content := range files {
		if err := fsutil.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return nil, err
		}
	}
	if dt := opts.DefaultTags; dt != nil && dt.Applied != nil {
		if err := applyDefaultTags(dir, *dt); err != nil {
			return nil, err
		}
	}
	// Resources whose type another provider of opts.ModuleProviders
	// implies, such as azapi_resource, need it required and configured
	// before init, which selects its newest release, pinned as the
	// root's own provider is.
	others := resourceProviders(g.imports, opts.ModuleProviders)
	if err := requireProviders(dir, others, opts.ModuleProviders); err != nil {
		return nil, err
	}
	if err := initTerraform(ctx, g.tf); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	if len(others) > 0 {
		if err := pinModuleProviders(dir, opts.ModuleProviders); err != nil {
			return nil, err
		}
	}
	return restoreAccess, nil
}

// planGenerated has Terraform generate generated.tf, imports again what
// it rejected that has an import ID to fall back on, and makes the edits
// that come before anything reads the file (see prepareGenerated). It
// returns why the file then needs another plan, if it does.
func (g *generation) planGenerated(ctx context.Context) ([]replanReason, error) {
	// Plan reports changes because every import is pending; only errors matter.
	// The release the lock file records decides how far the provider's
	// metadata goes (see schemaHints).
	hints := schemaHintsFor(g.dir, g.opts.SchemaMetadata)
	diags, _, err := plan(ctx, g.tf, tfexec.GenerateConfigOut(GeneratedFileName))
	if err != nil {
		return nil, fmt.Errorf("terraform plan: %w", err)
	}
	if _, err := os.Stat(g.generated); errors.Is(err, fs.ErrNotExist) {
		// Terraform stopped before generating anything, for example on the
		// provider configuration.
		if len(diags) == 0 {
			return nil, errors.New("terraform plan generated no configuration")
		}
		return nil, fmt.Errorf("terraform plan: %w", diagnosticsError(diags))
	} else if err != nil {
		return nil, err
	}
	// What Terraform rejected that has an import ID to fall back on is
	// imported again by it, before anything reads the configuration.
	if g.diags, err = retryFallbacks(ctx, g.tf, g.dir, g.imports, diags); err != nil {
		return nil, err
	}
	return prepareGenerated(g.generated, g.imports, g.opts, hints)
}

// hideSecrets moves the secrets in generated.tf into variables.
// Terraform wrote every value the provider doesn't mark sensitive into the
// file, secrets among them: they move before anything else reads it.
// Generate's own plans read their values from outside the output (see
// secretValues), which the run's secrets hold once there are any.
func (g *generation) hideSecrets() error {
	literal, warnings, err := literalSecretsToVariables(g.generated, g.r.taken)
	if err != nil {
		return err
	}
	g.literal, g.result.Warnings = literal, warnings
	if len(literal) == 0 {
		return nil
	}
	g.redacting.hide(literal)
	if g.r.secrets, err = newSecretValues(); err != nil {
		return err
	}
	if err := g.r.secrets.write(literal); err != nil {
		return err
	}
	return writeVariables(g.dir, literal, nil)
}

// replan plans the root again, for its errors and, if it has none, its
// plan (see generation.last).
func (g *generation) replan(ctx context.Context) error {
	shown, summary, ds, err := showPlanWithSummary(ctx, g.tf, g.r.secrets.options())
	if err != nil {
		return err
	}
	g.diags, g.last, g.planned = ds, nil, nil
	if len(ds) == 0 && shown != nil {
		if g.planned, err = rootFileContents(g.dir); err != nil {
			return err
		}
		g.last = &shownPlan{plan: shown, summary: summary}
	}
	return nil
}

// planUntilValid plans the root until it has no errors: the plan the
// configuration was generated with no longer holds once an edit changed
// it, for reasons. Terraform writes generated.tf even when the
// configuration it generated is invalid: it is repaired once (see
// repair), and the resources that still have errors then are left out
// (see Rejection). It fails on errors it can't attribute to a resource,
// and once maxPlanRounds plans still had errors.
func (g *generation) planUntilValid(ctx context.Context, reasons []replanReason) error {
	if len(reasons) > 0 {
		why := make([]string, len(reasons))
		for i, reason := range reasons {
			why[i] = string(reason)
		}
		log.Printf("%s: planning %s again: %s", g.dir, GeneratedFileName, strings.Join(why, ", "))
		if err := g.replan(ctx); err != nil {
			return err
		}
	}
	repaired := false
	for round := 0; ; round++ {
		errs, err := unresolved("terraform plan", g.dir, g.diags)
		if err != nil {
			// Errors can quote the values they reject, as below.
			return errors.New(redact(err.Error(), g.literal))
		}
		if len(errs) == 0 {
			return nil
		}
		if round == maxPlanRounds {
			var all []tfjson.Diagnostic
			for _, ds := range errs {
				all = append(all, ds...)
			}
			// Errors can quote the values they reject, as below.
			return fmt.Errorf("terraform plan: %s", redact(diagnosticsError(all).Error(), g.literal))
		}
		// Repair once; leave out what is left.
		changed := false
		if !repaired {
			repaired = true
			if changed, err = repair(ctx, g.tf, g.generated); err != nil {
				return err
			}
		}
		if !changed {
			rejections, err := reject(g.dir, errs, &g.rejected)
			if err != nil {
				return err
			}
			g.result.Rejected = append(g.result.Rejected, rejections...)
		}
		if err := g.replan(ctx); err != nil {
			return err
		}
	}
}

// leaveOut leaves out the resources the root doesn't import though their
// configuration is valid: those Terraform generated nothing for, and,
// unless opts.IncludeOwned, those another resource owns.
func (g *generation) leaveOut() error {
	// Terraform generates nothing for a resource its import finds gone, such
	// as a KMS key scheduled for deletion, and only a plan without
	// -generate-config-out says so: leave those out too.
	missing, err := unconfigured(g.dir)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		rejections, err := reject(g.dir, missing, &g.rejected)
		if err != nil {
			return err
		}
		g.result.Rejected = append(g.result.Rejected, rejections...)
	}
	if !g.opts.IncludeOwned {
		if g.result.Owned, err = leaveOutOwned(g.dir, g.opts.KeepOwned); err != nil {
			return err
		}
		for _, o := range g.result.Owned {
			g.leftOut[o.Address] = true
		}
	}
	return nil
}

// secretVariables makes the secret attributes of the resources the root
// keeps read from variables (see useVariables), and those whose values
// aren't secret after all literals again (see publicSecretsToLiterals).
func (g *generation) secretVariables(ctx context.Context) error {
	var kept []Secret
	for _, s := range withoutRejected(g.literal, g.result.Rejected) {
		if !g.leftOut[s.Address] {
			kept = append(kept, s)
		}
	}
	var err error
	if g.result.Secrets, err = useVariables(ctx, g.tf, g.dir, g.r.taken, kept, &g.result.Rejected, &g.rejected, g.r.secrets.options()); err != nil {
		return err
	}
	if g.result.Secrets, err = publicSecretsToLiterals(ctx, g.tf, g.dir, g.result.Secrets, g.r.secrets); err != nil {
		return err
	}
	if g.r.secrets != nil {
		// Only the variables still declared: Terraform warns about others.
		if err := g.r.secrets.write(g.result.Secrets); err != nil {
			return err
		}
	}
	return nil
}

// tidy post-processes the configuration (see postProcess), then groups
// what it moved into modules into the root's module calls, and gives the
// secrets the addresses their resources moved to.
func (g *generation) tidy(ctx context.Context) error {
	before, err := importTargets(g.dir)
	if err != nil {
		return err
	}
	// The last plan of planUntilValid is postProcess's first if nothing
	// changed since: the same files, and no secret variables, whose values
	// its plans get otherwise.
	last := g.last
	if last != nil && (g.r.secrets != nil || len(g.result.Secrets) > 0) {
		last = nil
	}
	if last != nil {
		same, err := sameFiles(g.dir, g.planned)
		if err != nil {
			return err
		}
		if !same {
			last = nil
		}
	}
	declined, err := postProcess(ctx, g.tf, g.dir, g.r, g.result.Secrets, last)
	if err != nil {
		return err
	}
	// Once the references are made: what is still a literal is no
	// resource's.
	if _, err := notePrincipals(g.generated); err != nil {
		return err
	}
	moved, err := movedAddresses(g.dir, before)
	if err != nil {
		return err
	}
	if g.result.Modules, err = moduleCalls(g.dir, moved, declined); err != nil {
		return err
	}
	for i, s := range g.result.Secrets {
		if to, ok := moved[s.Address]; ok {
			g.result.Secrets[i].Address = to
		}
	}
	return nil
}

// finish writes rejected.tf, settles what the root imports, and lays out
// its files: split by concern if opts.SplitFiles, with the moved and
// removed blocks the addresses the root had before call for, and the
// Terraform release they need.
func (g *generation) finish(ctx context.Context) error {
	opts, dir, result := g.opts, g.dir, g.result
	// Errors can quote the values they reject.
	for i := range result.Rejected {
		for j, e := range result.Rejected[i].Errors {
			result.Rejected[i].Errors[j] = redact(e, g.literal)
		}
	}
	if g.rejected.Len() > 0 {
		if err := fsutil.WriteFile(filepath.Join(dir, RejectedFileName), redactBytes(g.rejected.Bytes(), g.literal), 0o644); err != nil {
			return err
		}
	}
	for _, r := range result.Rejected {
		g.leftOut[r.Address] = true
	}
	for _, imp := range g.imports {
		if !g.leftOut[imp.Type+"."+imp.Name] {
			result.Imported = append(result.Imported, imp)
		}
	}
	var err error
	if opts.SplitFiles {
		if result.Files, err = splitFiles(dir, result.Imported, result.Modules); err != nil {
			return err
		}
	}
	if err := typeLiteralsOf(ctx, g.tf, dir); err != nil {
		return err
	}
	if err := layoutDir(dir); err != nil {
		return err
	}
	if result.Addresses, err = ImportedAddresses(dir); err != nil {
		return err
	}
	// Last, once the addresses are final: the gate's plans read the moved
	// and removed blocks too, and they may need a later release.
	result.Moves, result.Retyped, result.Unmoved = moves(opts.Previous, result.Addresses, callSources(result.Modules))
	result.Removed = removals(opts.Previous, result.Addresses)
	result.Arrived = arrived(opts.Arrivals, result.Addresses)
	if err := writeMoves(dir, result.Moves, result.Removed, result.Retyped); err != nil {
		return err
	}
	return requireMinimumVersion(dir)
}

// gate refines the finished root if opts.Refine is set, then gates it
// (see gateRefined).
func (g *generation) gate(ctx context.Context) error {
	var unrefined map[string][]byte
	warned := len(g.result.Warnings)
	if g.opts.Refine != nil {
		var err error
		if unrefined, err = refine(ctx, g.tf, g.dir, g.result, g.opts); err != nil {
			return err
		}
	}
	// The gate checks the modules the run may call, which for Add's are
	// fewer than the caller's (see adding).
	return gateRefined(ctx, g.tf, g.dir, g.result, g.opts, g.r.secrets, unrefined, warned)
}

// prepareGenerated makes the edits of generated.tf, at path, that come
// before anything reads it, and returns why the file then needs another
// plan, if any of them changed it. Terraform writes resources in the order
// its graph walk reaches them, which differs between runs: they are
// sorted, so the same estate always gives the same files. Then the
// arguments other resources manage are left out (see opts.Omit), and
// placeholders and lifecycles set. Last, arguments the provider says
// conflict, which fail validation, are settled now, as repair would once a
// plan reported them: after the omissions, so that of a pair the catalogue
// leaves one out of, such as an event rule's is_enabled beside its state,
// the other stays.
func prepareGenerated(path string, imports []Import, opts Options, hints schemaHints) ([]replanReason, error) {
	edits := []struct {
		reason replanReason
		edit   func() (bool, error)
	}{
		{replanSorted, func() (bool, error) { return sortResources(path) }},
		{replanOmitted, func() (bool, error) { return OmitArguments(path, withResourceOmissions(opts.Omit, imports)) }},
		{replanPlaceholders, func() (bool, error) { return setPlaceholders(path, opts.Placeholders) }},
		{replanLifecycles, func() (bool, error) { return setLifecycles(path, opts.Lifecycles) }},
		{replanConflicts, func() (bool, error) { return resolveConflicts(path, hints) }},
	}
	var reasons []replanReason
	for _, e := range edits {
		changed, err := e.edit()
		if err != nil {
			return nil, err
		}
		if changed {
			reasons = append(reasons, e.reason)
		}
	}
	return reasons, nil
}

// unresolved returns the errors in diags by resource, leaving out errors
// about secret attributes, which variables resolve (see useVariables). It
// fails on errors it can't attribute to a resource.
func unresolved(command, dir string, diags []tfjson.Diagnostic) (map[string][]tfjson.Diagnostic, error) {
	if len(diags) == 0 {
		return nil, nil
	}
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return nil, err
	}
	errs, general := byResource(diags, generated, imports)
	if len(general) > 0 {
		return nil, fmt.Errorf("%s: %w", command, diagnosticsError(diags))
	}
	secrets := findSecrets(generated)
	for addr, ds := range errs {
		var left []tfjson.Diagnostic
		for _, d := range ds {
			if !aboutSecret(d, secrets[addr]) {
				left = append(left, d)
			}
		}
		if len(left) == 0 {
			delete(errs, addr)
		} else {
			errs[addr] = left
		}
	}
	return errs, nil
}

// useVariables makes the secret attributes in generated.tf read from
// sensitive variables, declared in variables.tf with those of literal (the
// secrets infraharvest found in the configuration, which read from
// variables already), named other than the variables in taken, and
// validates the result. It leaves out resources that still don't validate,
// adding them to rejections and out. It returns literal's variables first.
// Secret attributes the imported objects hold no value for are left out of
// the configuration instead (see withoutUnset); vars are the options the
// plan that tells them apart needs.
func useVariables(ctx context.Context, tf Terraform, dir string, taken Names, literal []Secret, rejections *[]Rejection, out *bytes.Buffer, vars []tfexec.PlanOption) ([]Secret, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	found := findSecrets(generated)
	var schemas *tfjson.ProviderSchemas
	if len(found) > 0 {
		if schemas, err = tf.ProvidersSchema(ctx); err != nil {
			return nil, fmt.Errorf("terraform providers schema: %w", err)
		}
		found = withoutWriteOnly(found, schemas)
	}
	if len(found) > 0 {
		var unset bool
		if found, unset, err = withoutUnset(ctx, tf, found, schemas, vars); err != nil {
			return nil, err
		}
		if unset {
			if err := generated.save(); err != nil {
				return nil, err
			}
		}
	}
	if len(found) == 0 {
		// Written again, without the variables of resources left out since.
		return literal, writeVariables(dir, literal, schemas)
	}
	secrets := append(slices.Clone(literal), secretsToVariables(generated, found, withVariables(taken, literal))...)
	for i := len(literal); i < len(secrets); i++ {
		secrets[i].ty = attributeType(schemas, strings.SplitN(secrets[i].Address, ".", 2)[0], secrets[i].schemaPath)
	}
	if err := generated.save(); err != nil {
		return nil, err
	}
	if err := writeVariables(dir, secrets, schemas); err != nil {
		return nil, err
	}

	validation, err := tf.Validate(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform validate: %w", err)
	}
	if validation.Valid {
		return secrets, nil
	}
	errs, err := unresolved("terraform validate", dir, errorDiagnostics(validation.Diagnostics))
	if err != nil {
		return nil, err
	}
	rejected, err := reject(dir, errs, out)
	if err != nil {
		return nil, err
	}
	*rejections = append(*rejections, rejected...)
	kept := secrets[:0]
	for _, s := range secrets {
		if _, gone := errs[s.Address]; !gone {
			kept = append(kept, s)
		}
	}
	return kept, writeVariables(dir, kept, schemas)
}

// writeVariables writes variables.tf, or removes it if there are no secrets.
func writeVariables(dir string, secrets []Secret, schemas *tfjson.ProviderSchemas) error {
	path := filepath.Join(dir, VariablesFileName)
	if len(secrets) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	content, err := variablesFile(secrets, schemas)
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, content, 0o644)
}

// schemaCache is a Terraform that reads the providers' schemas once, until
// the next init, which may install other providers, such as a module's.
type schemaCache struct {
	Terraform
	schemas *tfjson.ProviderSchemas
	read    bool
}

func (c *schemaCache) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	c.schemas, c.read = nil, false
	return c.Terraform.Init(ctx, opts...)
}

func (c *schemaCache) ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error) {
	if c.read {
		return c.schemas, nil
	}
	schemas, err := c.Terraform.ProvidersSchema(ctx)
	if err != nil {
		return nil, err
	}
	c.schemas, c.read = schemas, true
	return schemas, nil
}

// environ returns the current environment as a map.
func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		for i := 1; i < len(kv); i++ { // start at 1: Windows has "=C:=C:\" entries
			if kv[i] == '=' {
				env[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return env
}

// rootFileContents returns the content of the regular files directly in dir, by
// name: the configuration a plan of dir reads, but for the providers and
// modules init installs.
func rootFileContents(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = content
	}
	return files, nil
}

// sameFiles reports whether the regular files directly in dir are those
// of files, with the same content (see rootFileContents).
func sameFiles(dir string, files map[string][]byte) (bool, error) {
	now, err := rootFileContents(dir)
	if err != nil {
		return false, err
	}
	return maps.EqualFunc(now, files, bytes.Equal), nil
}

// mergeEnv returns the environment base with over on top, for Terraform.
// Without base's TF_DATA_DIR, the process's: Terraform then keeps its
// working data in each root's .terraform, where the engine reads it, such
// as the modules init installed, and roots generating in parallel don't
// share one. over's, a directory of one Terraform's own, such as drift's
// for roots that may come from someone else (see
// DriftOptions.DataDir), is kept. Where
// variable names are case-insensitive (foldCase), as on Windows, a
// variable of over replaces base's of any case, so that Terraform doesn't
// get both, and the names of TF_ variables but input variables are upper
// case, so that tfexec.CleanEnv, which matches names exactly, finds those
// it manages.
func mergeEnv(base, over map[string]string, foldCase bool) map[string]string {
	key := func(name string) string { return name }
	if foldCase {
		key = strings.ToUpper
	}
	all := make(map[string]string, len(base)+len(over))
	names := map[string]string{} // by key, the name all has the variable under
	set := func(name, value string) {
		if old, ok := names[key(name)]; ok {
			delete(all, old)
		}
		names[key(name)] = name
		all[name] = value
	}
	for name, value := range base {
		if key(name) != key("TF_DATA_DIR") {
			set(name, value)
		}
	}
	for name, value := range over {
		set(name, value)
	}
	if foldCase {
		for name, value := range maps.Clone(all) {
			if upper := strings.ToUpper(name); upper != name && strings.HasPrefix(upper, "TF_") && !strings.HasPrefix(upper, "TF_VAR_") {
				delete(all, name)
				all[upper] = value
			}
		}
	}
	return all
}
