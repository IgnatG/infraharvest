// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package tfquery lists resources through Terraform's own list resources:
// it writes list blocks into a .tfquery.hcl file of a working directory of
// its own, runs terraform query -json there and records each result by its
// resource identity (see harvest.RecordIdentity), which the import then
// imports it by. It is the query service's listing (see
// harvest.QueryService), the same for every provider: what differs between
// them is their provider block and scope, which the caller passes.
package tfquery

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// Minimum is the first Terraform release with terraform query and list
// resources. OpenTofu has neither.
var Minimum = version.Must(version.NewVersion("1.14.0"))

// Binary is Terraform, at Minimum or newer: what a run that lists through
// Terraform finds or downloads (see tfrun.Binary.Find), to list and then
// to generate with.
func Binary() tfrun.Binary {
	b := tfrun.TerraformBinary
	b.Minimum = Minimum
	return b
}

// Limit is the most results a list block returns: Terraform stops a list
// at its limit, 100 unless the block says another, so each block sets this
// one, and a list that reaches it is reported, never taken for complete.
const Limit = 10000

// FileName is the file of the working directory that holds the list
// blocks; Terraform reads files named *.tfquery.hcl for terraform query.
const FileName = "list.tfquery.hcl"

// Request is one listing through Terraform's list resources.
type Request struct {
	// ExecPath is the Terraform binary, PluginCacheDir the plugin cache its
	// init shares, and Env what the provider adds to its environment,
	// such as its credentials.
	ExecPath, PluginCacheDir string
	Env                      map[string]string
	// Parent is where the working directory goes, such as the output
	// directory's .infraharvest: it is made private, and removed when the
	// listing is done.
	Parent string
	// Provider is the provider whose list resources list, with the block
	// that configures it for the scope.
	Provider engine.Provider
	// ProviderName is the provider's short name the resources are
	// recorded with (see harvest.Resource.Provider).
	ProviderName string
	// Types are the resource types to list, each with a list block.
	Types []string
	// Config are arguments of a list block's config that narrow it to the
	// scope (see harvest.ProviderWithQueryScope); each is set where the
	// type's list resource takes it.
	Config map[string]string
	// InScope, if set, keeps of the results those in the scope, by their
	// identity.
	InScope func(identity map[string]string) bool
}

// terraform is what List runs Terraform with: *tfrun.Runner, or a fake.
type terraform interface {
	Init(ctx context.Context, opts ...tfexec.InitOption) error
	ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error)
	QueryJSON(ctx context.Context, opts ...tfexec.QueryOption) (iter.Seq[tfexec.NextMessage], error)
}

// runner makes the Terraform runner of a working directory, and
// versionOf reads a binary's version; tests replace them.
type runner struct {
	newTerraform func(dir, execPath, pluginCacheDir string, env map[string]string) (terraform, error)
	versionOf    func(ctx context.Context, execPath string) (*version.Version, error)
}

var defaultRunner = runner{
	newTerraform: func(dir, execPath, pluginCacheDir string, env map[string]string) (terraform, error) {
		return tfrun.New(dir, execPath, pluginCacheDir, env)
	},
	versionOf: tfrun.BinaryVersion,
}

// List lists req.Types through Terraform's list resources, identities
// only, and returns what they found in the scope. Types the provider has
// no list resource for, or whose list resource needs arguments
// infraharvest has no value for, such as the API of an API Gateway
// resource, fail the listing, naming them. A type whose list failed, or
// reached Limit, leaves the others' results standing: List returns them
// with a partial error (see harvest.Partial) that names it.
func List(ctx context.Context, req Request) ([]harvest.Resource, error) {
	return defaultRunner.list(ctx, req)
}

func (r runner) list(ctx context.Context, req Request) ([]harvest.Resource, error) {
	v, err := r.versionOf(ctx, req.ExecPath)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(v); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(req.Parent, 0o755); err != nil {
		return nil, err
	}
	// MkdirTemp makes it private, mode 0700: the provider's block and
	// Terraform's working files are the user's alone.
	dir, err := os.MkdirTemp(req.Parent, "query-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("%s: query: removing %s: %v", req.ProviderName, dir, err)
		}
	}()
	providers, err := engine.ProvidersFile(req.Provider)
	if err != nil {
		return nil, err
	}
	for name, content := range map[string][]byte{
		engine.VersionsFileName:  engine.VersionsFile("", req.Provider),
		engine.ProvidersFileName: providers,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return nil, err
		}
	}
	tf, err := r.newTerraform(dir, req.ExecPath, req.PluginCacheDir, req.Env)
	if err != nil {
		return nil, err
	}
	if err := tf.Init(ctx); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform providers schema: %w", err)
	}
	lists, err := listSchemas(schemas, req.Provider.Source)
	if err != nil {
		return nil, err
	}
	blocks, err := listBlocks(req, lists)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), blocks, 0o600); err != nil {
		return nil, err
	}
	messages, err := tf.QueryJSON(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform query: %w", err)
	}
	return collect(messages, req)
}

// checkVersion fails for a Terraform older than Minimum.
func checkVersion(v *version.Version) error {
	if v.LessThan(Minimum) {
		return fmt.Errorf("listing through Terraform's list resources needs Terraform %s or newer (terraform query); this is %s: pass a newer one with --terraform-path", Minimum, v)
	}
	return nil
}

// listSchemas returns the list resources of the provider at source, such
// as hashicorp/aws, by type: those of the one provider in schemas,
// whatever its address, or those of the provider whose address ends in
// source.
func listSchemas(schemas *tfjson.ProviderSchemas, source string) (map[string]*tfjson.Schema, error) {
	if schemas != nil {
		for address, p := range schemas.Schemas {
			if len(schemas.Schemas) == 1 || address == source || strings.HasSuffix(address, "/"+source) {
				return p.ListResourceSchemas, nil
			}
		}
	}
	return nil, fmt.Errorf("terraform providers schema has no schema of %s", source)
}

// listBlocks writes a list block for each of req's types: identities
// only, as the import reads each resource when it plans anyway, with
// Limit, through the provider block of the working directory, and with the
// arguments of req.Config the type's list resource takes. It fails,
// naming them, for the types the provider has no list resource for, and
// those whose list resource requires arguments.
func listBlocks(req Request, lists map[string]*tfjson.Schema) ([]byte, error) {
	var missing, needs []string
	for _, typ := range req.Types {
		s, ok := lists[typ]
		if !ok {
			missing = append(missing, typ)
			continue
		}
		if required := requiredArguments(s); len(required) > 0 {
			needs = append(needs, fmt.Sprintf("%s (%s)", typ, strings.Join(required, ", ")))
		}
	}
	var errs []error
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("%s has no list resource for %s: terraform query can't list them", req.Provider.Source, strings.Join(missing, ", ")))
	}
	if len(needs) > 0 {
		errs = append(errs, fmt.Errorf("the list resources of %s need arguments infraharvest has no value for", strings.Join(needs, ", ")))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	f := hclwrite.NewEmptyFile()
	body := f.Body()
	for i, typ := range req.Types {
		if i > 0 {
			body.AppendNewline()
		}
		block := body.AppendNewBlock("list", []string{typ, label}).Body()
		block.SetAttributeRaw("provider", hclwrite.TokensForIdentifier(req.Provider.Name))
		block.SetAttributeValue("include_resource", cty.False)
		block.SetAttributeValue("limit", cty.NumberIntVal(Limit))
		var config map[string]string
		for _, k := range slices.Sorted(maps.Keys(req.Config)) {
			if s := lists[typ]; s.Block != nil && s.Block.Attributes[k] != nil {
				if config == nil {
					config = map[string]string{}
				}
				config[k] = req.Config[k]
			}
		}
		if len(config) > 0 {
			c := block.AppendNewBlock("config", nil).Body()
			for _, k := range slices.Sorted(maps.Keys(config)) {
				c.SetAttributeValue(k, cty.StringVal(config[k]))
			}
		}
	}
	return hclwrite.Format(f.Bytes()), nil
}

// label is the label of every list block: each lists a type of its own,
// which its address, list.<type>.all, names.
const label = "all"

// requiredArguments returns the arguments a list resource's config
// requires, sorted: in its attributes, and its nested blocks of at least
// one item.
func requiredArguments(s *tfjson.Schema) []string {
	if s == nil || s.Block == nil {
		return nil
	}
	var required []string
	for name, a := range s.Block.Attributes {
		if a != nil && a.Required {
			required = append(required, name)
		}
	}
	for name, b := range s.Block.NestedBlocks {
		if b != nil && b.MinItems > 0 {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	return required
}

// collect reads terraform query's messages: the resources each list found,
// in the scope (see Request.InScope), how many each returned, and the
// errors. The errors of a list block fail its type alone, as do those of a
// list that reached Limit; others fail the listing.
func collect(messages iter.Seq[tfexec.NextMessage], req Request) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	totals := map[string]int{}
	failed := map[string][]string{}
	var errs []error
	var exit error
	outOfScope := 0
	for m := range messages {
		if m.Err != nil {
			// The last message: terraform query's exit, failed.
			exit = m.Err
			continue
		}
		switch msg := m.Msg.(type) {
		case tfjson.ListResourceFoundMessage:
			found := msg.ListResourceFound
			identity, ok := tftypes.IdentityValues(found.Identity)
			if !ok || len(identity) == 0 {
				log.Printf("%s: query: leaving out %s %s: its identity %v can't be written as an import block's", req.ProviderName, found.ResourceType, found.DisplayName, found.Identity)
				continue
			}
			if req.InScope != nil && !req.InScope(identity) {
				outOfScope++
				continue
			}
			resources = append(resources, harvest.RecordIdentity(identity, found.DisplayName, found.ResourceType, req.ProviderName))
		case tfjson.ListCompleteMessage:
			totals[msg.ListComplete.ResourceType] = msg.ListComplete.Total
		case tfjson.DiagnosticLogMessage:
			d := msg.Diagnostic
			if d.Severity != tfjson.DiagnosticSeverityError {
				continue
			}
			text := strings.TrimSpace(d.Summary + ": " + d.Detail)
			if typ, ok := listType(d.Address); ok {
				failed[typ] = append(failed[typ], text)
				continue
			}
			errs = append(errs, errors.New(text))
		}
	}
	if outOfScope > 0 {
		log.Printf("%s: query: %d resources Terraform listed are outside this scope, which another one imports", req.ProviderName, outOfScope)
	}
	var partial []error
	for _, typ := range req.Types {
		if msgs := failed[typ]; len(msgs) > 0 {
			partial = append(partial, fmt.Errorf("%s: %s", typ, strings.Join(slices.Compact(msgs), "; ")))
		}
		if totals[typ] >= Limit {
			log.Printf("%s: query: %s: the list stopped at its limit of %d results, so there may be more than it found", req.ProviderName, typ, Limit)
			partial = append(partial, fmt.Errorf("%s: the list stopped at its limit of %d results, so there may be more", typ, Limit))
		}
	}
	if len(errs) > 0 || (exit != nil && len(failed) == 0) {
		return nil, fmt.Errorf("terraform query: %w", errors.Join(append(errs, exit)...))
	}
	if len(partial) > 0 {
		return resources, harvest.Partial(errors.Join(partial...))
	}
	return resources, nil
}

// listType returns the type of the list block at address, such as
// list.aws_ebs_volume.all, or false for any other address.
func listType(address string) (string, bool) {
	parts := strings.Split(address, ".")
	if len(parts) != 3 || parts[0] != "list" {
		return "", false
	}
	return parts[1], true
}
