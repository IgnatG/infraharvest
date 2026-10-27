// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// driftAllowed returns what the drift tools run Terraform with: the
// providers infraharvest generates roots with, by source address, such
// as hashicorp/aws, each provider's own and those its curated modules
// use (see harvest.ProviderWithModuleProviders); and the curated modules
// a root may call, by registry address without a submodule, such as
// terraform-aws-modules/vpc/aws. They are those of the providers
// compiled in, as the binary the tools run generates no others.
var driftAllowed = sync.OnceValues(func() (sources, modules map[string]bool) {
	sources, modules = map[string]bool{}, map[string]bool{}
	for _, name := range providers.Names() {
		p := providers.New(name)
		source := p.Profile().Source
		if source == "" {
			source = "hashicorp/" + p.Name()
		}
		sources[providerAddress(source)] = true
		if m, ok := p.(harvest.ProviderWithModuleProviders); ok {
			for _, mp := range m.ModuleProviders() {
				sources[providerAddress(mp.Source)] = true
			}
		}
		for _, a := range adapters.For(p.Profile().Modules) {
			modules[moduleAddress(a.Source)] = true
		}
	}
	return sources, modules
})

// refusedProvider is a provider the drift tools never run, whichever
// providers are compiled in: its external data source runs any program
// the configuration names.
const refusedProvider = "hashicorp/external"

// publicRegistries are the hosts a provider or module address may name,
// the same as naming none.
var publicRegistries = []string{"registry.terraform.io/", "registry.opentofu.org/"}

// providerAddress returns a provider's source address as driftAllowed
// has it: lower case, without the public registry's host.
func providerAddress(source string) string {
	s := strings.ToLower(strings.TrimSpace(source))
	for _, host := range publicRegistries {
		s = strings.TrimPrefix(s, host)
	}
	return s
}

// moduleAddress returns a registry module's source as driftAllowed has
// it: lower case, without the public registry's host or a submodule.
func moduleAddress(source string) string {
	s, _, _ := strings.Cut(providerAddress(source), "//")
	return s
}

// checkDriftRoots refuses the configuration under out, the output
// directory of a drift tool, unless every provider it uses is one
// infraharvest generates roots with, and every module it calls is under
// out or a curated module (see driftAllowed). drift runs Terraform's init
// and plan on the roots, which install and run their providers and
// modules with the server's credentials: a provider is a program, and the
// external data source runs any, so roots from a cloned repository could
// run code of an agent's choosing. A module from anywhere else, such as
// git, could bring providers of its own, which can't be checked before
// init downloads it. Each .tf, .tf.json, .tofu and .tofu.json file is
// read, override files too, as Terraform and OpenTofu read them, and each
// directory a local module call leads to, the walk aside.
//
// The roots' .terraform directories aren't read: the tools run drift
// with --untrusted-roots, so Terraform ignores them, and installs
// providers from their registries alone. A terraform.d directory, a
// filesystem mirror Terraform would install providers from, is refused
// all the same, as is a module call into either.
func checkDriftRoots(out string) error {
	var dirs []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".terraform":
				return filepath.SkipDir
			case "terraform.d":
				return fmt.Errorf("%s is a terraform.d directory, which Terraform installs providers from", relativeTo(out, path))
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// A walk doesn't follow it, and Terraform does.
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				return fmt.Errorf("%s is a symbolic link to a directory, which this check doesn't follow", relativeTo(out, path))
			}
		}
		if configFile(d.Name()) && !slices.Contains(dirs, filepath.Dir(path)) {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		// No roots: drift says so.
		return nil
	}
	if err != nil {
		return err
	}
	checked := map[string]bool{}
	for len(dirs) > 0 {
		dir := dirs[0]
		dirs = dirs[1:]
		if checked[dir] {
			continue
		}
		checked[dir] = true
		modules, err := checkModuleDir(out, dir)
		if err != nil {
			return err
		}
		dirs = append(dirs, modules...)
	}
	return nil
}

// configFile reports whether name is a file Terraform or OpenTofu reads
// configuration from.
func configFile(name string) bool {
	for _, ext := range []string{".tf", ".tf.json", ".tofu", ".tofu.json"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// moduleUses is what the configuration files of one module directory,
// a root or a module it calls, use: the providers their
// required_providers declare, each with where, the local names their
// blocks name, each with where, and the local modules they call.
type moduleUses struct {
	declared []declaredProvider
	named    map[string]hcl.Range
	// modules are the directories of the local modules it calls.
	modules []string
}

// declaredProvider is a provider required_providers declares: its local
// name and source address, where.
type declaredProvider struct {
	name, source string
	at           hcl.Range
}

// The blocks of a module that name providers or modules.
var driftModuleSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
	{Type: "terraform"},
	{Type: "provider", LabelNames: []string{"name"}},
	{Type: "resource", LabelNames: []string{"type", "name"}},
	{Type: "data", LabelNames: []string{"type", "name"}},
	{Type: "ephemeral", LabelNames: []string{"type", "name"}},
	{Type: "action", LabelNames: []string{"type", "name"}},
	{Type: "module", LabelNames: []string{"name"}},
	{Type: "check", LabelNames: []string{"name"}},
	{Type: "import"},
}}

// checkModuleDir checks the configuration files of the module in dir,
// under out (see checkDriftRoots), and returns the directories of the
// local modules it calls. A provider a block names without
// required_providers declaring it is hashicorp/<name>, as Terraform takes
// it. A provider-defined function's provider has to be declared, so it is
// checked there.
func checkModuleDir(out, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	uses := &moduleUses{named: map[string]hcl.Range{}}
	parser := hclparse.NewParser()
	for _, e := range entries {
		if e.IsDir() || !configFile(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var f *hcl.File
		var diags hcl.Diagnostics
		if strings.HasSuffix(e.Name(), ".json") {
			f, diags = parser.ParseJSON(content, relativeTo(out, path))
		} else {
			f, diags = parser.ParseHCL(content, relativeTo(out, path))
		}
		if diags.HasErrors() {
			return nil, diags
		}
		if err := uses.read(out, dir, f.Body); err != nil {
			return nil, err
		}
	}
	// Another file, such as an override file, may declare a provider
	// again: each declaration is checked.
	declared := map[string]bool{}
	for _, d := range uses.declared {
		if err := allowedProvider(d.source, d.at, "declares the provider "+d.name+" as"); err != nil {
			return nil, err
		}
		declared[d.name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(uses.named)) {
		if declared[name] {
			continue
		}
		if err := allowedProvider("hashicorp/"+name, uses.named[name], "uses the provider"); err != nil {
			return nil, err
		}
	}
	return uses.modules, nil
}

// allowedProvider returns an error, at where the configuration says
// what, unless source is a provider driftAllowed has, and not
// refusedProvider.
func allowedProvider(source string, at hcl.Range, what string) error {
	sources, _ := driftAllowed()
	address := providerAddress(source)
	if address != refusedProvider && sources[address] {
		return nil
	}
	return fmt.Errorf("%s: %s %s, which isn't a provider infraharvest generates roots with (%s)", where(at), what, source, strings.Join(slices.Sorted(maps.Keys(sources)), ", "))
}

// read adds what body, of a configuration file of the module in dir,
// declares and names to u, and checks the sources of the modules it
// calls.
func (u *moduleUses) read(out, dir string, body hcl.Body) error {
	content, _, diags := body.PartialContent(driftModuleSchema)
	if diags.HasErrors() {
		return diags
	}
	for _, b := range content.Blocks {
		var err error
		switch b.Type {
		case "terraform":
			err = u.readTerraform(b.Body)
		case "provider":
			u.name(b.Labels[0], b.LabelRanges[0])
		case "resource", "data", "ephemeral", "action":
			err = u.readResource(b)
		case "module":
			err = u.readModule(out, dir, b)
		case "check":
			// Its scoped data sources.
			nested, _, diags := b.Body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "data", LabelNames: []string{"type", "name"}}}})
			if diags.HasErrors() {
				return diags
			}
			for _, d := range nested.Blocks {
				if err := u.readResource(d); err != nil {
					return err
				}
			}
		case "import":
			err = u.readProviderArgument(b.Body)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// name records that the module names the provider local, at where.
func (u *moduleUses) name(local string, where hcl.Range) {
	if _, ok := u.named[local]; !ok {
		u.named[local] = where
	}
}

// readTerraform reads the required_providers of a terraform block: each
// provider's source, or hashicorp/<name> without one, as for a version
// constraint alone.
func (u *moduleUses) readTerraform(body hcl.Body) error {
	content, _, diags := body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "required_providers"}}})
	if diags.HasErrors() {
		return diags
	}
	for _, b := range content.Blocks {
		attrs, diags := b.Body.JustAttributes()
		if diags.HasErrors() {
			return diags
		}
		for _, name := range slices.Sorted(maps.Keys(attrs)) {
			attr := attrs[name]
			v, diags := attr.Expr.Value(nil)
			if diags.HasErrors() {
				return diags
			}
			source := "hashicorp/" + name
			if v.Type().IsObjectType() && v.Type().HasAttribute("source") {
				s := v.GetAttr("source")
				if s.IsNull() || !s.IsKnown() || s.Type() != cty.String {
					return fmt.Errorf("%s: the provider %s has a source that isn't a string", where(attr.Range), name)
				}
				source = s.AsString()
			}
			u.declared = append(u.declared, declaredProvider{name: name, source: source, at: attr.Range})
		}
	}
	return nil
}

// readResource reads the provider a resource, data source, ephemeral
// resource or action block uses: its provider argument's, else the one
// its type implies, the part before the first underscore.
func (u *moduleUses) readResource(b *hcl.Block) error {
	content, _, diags := b.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "provider"}}})
	if diags.HasErrors() {
		return diags
	}
	if _, ok := content.Attributes["provider"]; ok {
		return u.readProviderArgument(b.Body)
	}
	implied, _, _ := strings.Cut(b.Labels[0], "_")
	u.name(implied, b.LabelRanges[0])
	return nil
}

// readProviderArgument reads the provider argument of body, such as
// aws.west, if it has one.
func (u *moduleUses) readProviderArgument(body hcl.Body) error {
	content, _, diags := body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "provider"}}})
	if diags.HasErrors() {
		return diags
	}
	attr, ok := content.Attributes["provider"]
	if !ok {
		return nil
	}
	traversal, diags := hcl.AbsTraversalForExpr(attr.Expr)
	if diags.HasErrors() {
		return diags
	}
	u.name(traversal.RootName(), attr.Range)
	return nil
}

// readModule checks the source of a module call (see moduleSource) and
// reads the providers its providers argument passes it.
func (u *moduleUses) readModule(out, dir string, b *hcl.Block) error {
	content, _, diags := b.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "source"}, {Name: "providers"}}})
	if diags.HasErrors() {
		return diags
	}
	attr, ok := content.Attributes["source"]
	if !ok {
		return fmt.Errorf("%s: the module %s has no source", where(b.DefRange), b.Labels[0])
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() {
		return diags
	}
	if v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return fmt.Errorf("%s: the module %s has a source that isn't a string", where(attr.Range), b.Labels[0])
	}
	local, err := moduleSource(out, dir, v.AsString())
	if err != nil {
		return fmt.Errorf("%s: the module %s's source %w", where(attr.Range), b.Labels[0], err)
	}
	if local != "" {
		u.modules = append(u.modules, local)
	}
	providersAttr, ok := content.Attributes["providers"]
	if !ok {
		return nil
	}
	pairs, diags := hcl.ExprMap(providersAttr.Expr)
	if diags.HasErrors() {
		return diags
	}
	for _, pair := range pairs {
		traversal, diags := hcl.AbsTraversalForExpr(pair.Value)
		if diags.HasErrors() {
			return diags
		}
		u.name(traversal.RootName(), pair.Value.Range())
	}
	return nil
}

// moduleSource checks the source of a module the module in dir calls,
// and returns its directory for a local one: a path to a directory under
// out, but in a .terraform or terraform.d directory, which checkDriftRoots
// checks in turn; or a curated module from the public registry, whose
// providers are infraharvest's. Any other source, such as git or a URL,
// could bring providers of its own, which can't be checked before init
// downloads it.
func moduleSource(out, dir, source string) (string, error) {
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		local := filepath.Join(dir, filepath.FromSlash(source))
		if checkWithin("module", out, local) != nil {
			return "", fmt.Errorf("is %s, outside the output directory", source)
		}
		for _, part := range strings.Split(relativeTo(out, local), "/") {
			if part == ".terraform" || part == "terraform.d" {
				return "", fmt.Errorf("is %s, in a %s directory, which drift doesn't check", source, part)
			}
		}
		return local, nil
	}
	if _, modules := driftAllowed(); modules[moduleAddress(source)] {
		return "", nil
	}
	return "", fmt.Errorf("is %s, which is neither under the output directory nor a curated module infraharvest calls, and could bring providers of its own", source)
}

// relativeTo returns path relative to out, for a message, or path if it
// can't be.
func relativeTo(out, path string) string {
	if rel, err := filepath.Rel(out, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

// where says where r is, as file:line.
func where(r hcl.Range) string {
	return fmt.Sprintf("%s:%d", r.Filename, r.Start.Line)
}
