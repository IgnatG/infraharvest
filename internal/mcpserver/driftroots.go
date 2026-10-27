// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/driftpolicy"
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
// out or a curated module (see driftAllowed), and returns the digest of
// the files it read (see driftpolicy.ReadRoots), which the user's
// confirmation covers. drift runs Terraform's init and plan on the
// roots, which install and run their providers and modules with the
// server's credentials: a provider is a program, and the external data
// source runs any, so roots from a cloned repository could run code of an
// agent's choosing. A module from anywhere else, such as git, could bring
// providers of its own, which can't be checked before init downloads it.
// Each .tf, .tf.json, .tofu and .tofu.json file is read, override files
// too, as Terraform and OpenTofu read them, once: the digest is of the
// bytes checked, never of a second read a file could have changed by.
//
// No expression may call a function that reads a local file (see
// checkFileFunctions).
//
// The roots' .terraform directories aren't read: the tools run drift
// with --untrusted-roots, so Terraform ignores them, and installs
// providers from their registries alone. A terraform.d directory, a
// filesystem mirror Terraform would install providers from, is refused
// all the same, as is a module call into either.
func checkDriftRoots(out string) (string, error) {
	parser := hclparse.NewParser()
	// The configuration files of each directory, in the order found.
	files := map[string][]*hcl.File{}
	// The backend settings of the environments of each root they share,
	// by the root's directory (see rootOfBackendConfig).
	backendConfigs := map[string][]*hcl.File{}
	var dirs []string
	digest, err := driftpolicy.ReadRoots(out, func(path string, content []byte) error {
		name := filepath.Base(path)
		if root := rootOfBackendConfig(path); root != "" {
			f, diags := parser.ParseHCL(content, relativeTo(out, path))
			if diags.HasErrors() {
				return diags
			}
			backendConfigs[root] = append(backendConfigs[root], f)
			return nil
		}
		if !configFile(name) {
			return nil
		}
		var f *hcl.File
		var diags hcl.Diagnostics
		var jsonContent []byte
		if strings.HasSuffix(name, ".json") {
			f, diags = parser.ParseJSON(content, relativeTo(out, path))
			jsonContent = content
		} else {
			f, diags = parser.ParseHCL(content, relativeTo(out, path))
		}
		if diags.HasErrors() {
			return diags
		}
		if err := checkFileFunctions(f, jsonContent); err != nil {
			return err
		}
		dir := filepath.Dir(path)
		if _, ok := files[dir]; !ok {
			dirs = append(dirs, dir)
		}
		files[dir] = append(files[dir], f)
		return nil
	})
	if err != nil {
		return "", err
	}
	checked := map[string]bool{}
	for len(dirs) > 0 {
		dir, err := walkedDir(out, dirs[0], files)
		if err != nil {
			return "", err
		}
		dirs = dirs[1:]
		if checked[dir] {
			continue
		}
		checked[dir] = true
		modules, err := checkModuleDir(out, dir, files[dir], backendConfigs[dir])
		if err != nil {
			return "", err
		}
		dirs = append(dirs, modules...)
	}
	return digest, nil
}

// walkedDir returns dir, a module directory under out, as the walk of
// checkDriftRoots named it, a key of files, the configuration files it
// read by directory: a local module's directory is under out, outside
// its .terraform and terraform.d directories (see moduleSource), so the
// walk read it, but a module call may name it otherwise, such as in
// another case, where the file system ignores it. A directory that isn't
// there, or that has configuration files the walk didn't read, is
// refused: they would go unchecked, and out of the digest.
func walkedDir(out, dir string, files map[string][]*hcl.File) (string, error) {
	if _, ok := files[dir]; ok {
		return dir, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("the module directory %s can't be checked: %w", relativeTo(out, dir), err)
	}
	for _, walked := range slices.Sorted(maps.Keys(files)) {
		if w, err := os.Stat(walked); err == nil && os.SameFile(info, w) {
			return walked, nil
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if configFile(e.Name()) {
			return "", fmt.Errorf("the module directory %s has configuration files the check didn't read where it walked the output directory", relativeTo(out, dir))
		}
	}
	// No configuration to check.
	return dir, nil
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

// fileFunctions are Terraform's functions that read a local file, and
// templatestring, which renders a template from a string, whose own
// function calls a check of the configuration can't see. infraharvest
// generates roots with none of them.
var fileFunctions = map[string]bool{
	"file": true, "filebase64": true, "fileexists": true, "fileset": true,
	"filemd5": true, "filesha1": true, "filesha256": true, "filesha512": true,
	"filebase64sha256": true, "filebase64sha512": true,
	"templatefile": true, "templatestring": true,
}

// checkFileFunctions refuses f, a configuration file the drift tools
// parsed, if any expression in it calls one of fileFunctions, with or
// without the core:: namespace: the call could put a local file, such
// as ~/.aws/credentials, into an argument, which plan would show as
// drift, to the agent. jsonContent is the content of a JSON
// file, whose string values, and keys, Terraform takes as templates; nil
// for native syntax. An expression that doesn't parse is refused, as
// what it calls can't be checked. A var file or backend settings file
// isn't checked: Terraform evaluates its values without functions.
func checkFileFunctions(f *hcl.File, jsonContent []byte) error {
	if jsonContent != nil {
		return checkJSONFileFunctions(f.Body.MissingItemRange().Filename, jsonContent)
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return fmt.Errorf("%s: the file's expressions can't be checked", f.Body.MissingItemRange().Filename)
	}
	return fileFunctionCalls(body)
}

// checkJSONFileFunctions checks each string, value or key, of content,
// the JSON file filename, as a template (see checkFileFunctions).
func checkJSONFileFunctions(filename string, content []byte) error {
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", filename, err)
		}
		s, ok := tok.(string)
		if !ok {
			continue
		}
		// A JSON string has no newline of its own: the line its end is
		// on is the line it is on.
		line := 1 + bytes.Count(content[:dec.InputOffset()], []byte("\n"))
		expr, diags := hclsyntax.ParseTemplate([]byte(s), filename, hcl.Pos{Line: line, Column: 1})
		if diags.HasErrors() {
			return fmt.Errorf("%s:%d: a string that isn't a template whose function calls can be checked: %w", filename, line, diags)
		}
		if err := fileFunctionCalls(expr); err != nil {
			return err
		}
	}
}

// fileFunctionCalls refuses node if it, or anything in it, calls one of
// fileFunctions.
func fileFunctionCalls(node hclsyntax.Node) error {
	var err error
	_ = hclsyntax.VisitAll(node, func(n hclsyntax.Node) hcl.Diagnostics {
		call, ok := n.(*hclsyntax.FunctionCallExpr)
		if ok && err == nil && fileFunctions[strings.TrimPrefix(call.Name, "core::")] {
			err = fmt.Errorf("%s: calls %s, which reads a local file, or renders a template that could: plan could show the agent the file's content as drift", where(call.NameRange), call.Name)
		}
		return nil
	})
	return err
}

// moduleUses is what the configuration files of one module directory,
// a root or a module it calls, use: the providers their
// required_providers declare, each with where, the local names their
// blocks name, each with where, the local modules they call, and the
// blocks that configure providers and the backend.
type moduleUses struct {
	declared []declaredProvider
	named    map[string]hcl.Range
	// modules are the directories of the local modules it calls.
	modules []string
	// providers are its provider blocks, and backends the backend blocks
	// of its terraform blocks.
	providers, backends []*hcl.Block
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

// checkModuleDir checks files, the configuration files of the module in
// dir, under out (see checkDriftRoots), with backendConfigs, the backend
// settings of its environments where it is a root they share, and
// returns the directories of the local modules it calls. A provider a
// block names without required_providers declaring it is
// hashicorp/<name>, as Terraform takes it. A provider-defined function's
// provider has to be declared, so it is checked there. Its provider and
// backend blocks, and the backend settings, may set only what
// infraharvest generates them with (see checkSettings).
func checkModuleDir(out, dir string, files, backendConfigs []*hcl.File) ([]string, error) {
	uses := &moduleUses{named: map[string]hcl.Range{}}
	for _, f := range files {
		if err := uses.read(out, dir, f.Body); err != nil {
			return nil, err
		}
	}
	// Another file, such as an override file, may declare a provider
	// again: each declaration is checked.
	declared := map[string][]string{}
	for _, d := range uses.declared {
		if err := allowedProvider(d.source, d.at, "declares the provider "+d.name+" as"); err != nil {
			return nil, err
		}
		declared[d.name] = append(declared[d.name], d.source)
	}
	for _, name := range slices.Sorted(maps.Keys(uses.named)) {
		if declared[name] != nil {
			continue
		}
		if err := allowedProvider("hashicorp/"+name, uses.named[name], "uses the provider"); err != nil {
			return nil, err
		}
	}
	for _, b := range uses.providers {
		sources := declared[b.Labels[0]]
		if sources == nil {
			sources = []string{"hashicorp/" + b.Labels[0]}
		}
		for _, source := range sources {
			if err := checkSettings(b.Body, providerSettings[providerAddress(source)], "the provider "+b.Labels[0]); err != nil {
				return nil, err
			}
		}
	}
	for _, b := range uses.backends {
		what := "the " + b.Labels[0] + " backend"
		s, ok := backendSettings[b.Labels[0]]
		if !ok {
			return nil, fmt.Errorf("%s: %s, which infraharvest doesn't generate roots with (%s)", where(b.DefRange), what, strings.Join(slices.Sorted(maps.Keys(backendSettings)), ", "))
		}
		// drift initialises the backend with the settings of the
		// environment it checks, as -backend-config.
		for _, body := range append([]hcl.Body{b.Body}, bodies(backendConfigs)...) {
			if err := checkSettings(body, s, what); err != nil {
				return nil, err
			}
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

// settings are the arguments, and the nested blocks with theirs, that
// infraharvest generates a provider or backend block with (see
// checkSettings).
type settings struct {
	arguments []string
	blocks    map[string]*settings
}

// providerSettings are what infraharvest configures each provider of
// driftAllowed with, by source address as driftAllowed has it: the
// providers' own blocks (each provider's ProviderBlock, written without
// credentials by the engine's ProvidersFile, and in a root environments
// share with the region and what differs as variables), those of the
// providers curated modules use (ModuleProviders), and the second
// configuration of a provider, with an alias, for the resources without
// its default tags. A custom Azure cloud's endpoints for azapi aren't
// among them: they would send the requests elsewhere.
var providerSettings = map[string]*settings{
	"hashicorp/aws": {
		arguments: []string{"alias", "allowed_account_ids", "region"},
		blocks: map[string]*settings{
			"assume_role":  {arguments: []string{"role_arn"}},
			"default_tags": {arguments: []string{"tags"}},
		},
	},
	"hashicorp/azurerm":     {arguments: []string{"alias", "subscription_id"}, blocks: map[string]*settings{"features": {}}},
	"azure/azapi":           {arguments: []string{"alias", "subscription_id"}},
	"hashicorp/google":      {arguments: []string{"add_terraform_attribution_label", "alias", "default_labels", "project", "region"}},
	"hashicorp/google-beta": {arguments: []string{"add_terraform_attribution_label", "alias", "default_labels", "project", "region"}},
}

// backendSettings are what infraharvest configures each backend it
// generates roots with with (see config.Backend.File), in its block and
// in an environment's backend settings, by type.
var backendSettings = map[string]*settings{
	"s3":      {arguments: []string{"bucket", "encrypt", "key", "kms_key_id", "region", "use_lockfile"}},
	"azurerm": {arguments: []string{"container_name", "key", "resource_group_name", "storage_account_name"}},
	"gcs":     {arguments: []string{"bucket", "prefix"}},
}

// checkSettings refuses body, the body of what, a provider or backend
// block or a nested block of one, or backend settings, unless it sets
// only what s has, at any depth. Anything else could have a provider or
// backend run a program, such as the AWS shared configuration's
// credential_process, or send its requests, with the credentials, to
// another host, such as through endpoints or a CA bundle; which settings
// are safe isn't for the check to judge, so it allows only those
// infraharvest generates.
func checkSettings(body hcl.Body, s *settings, what string) error {
	if s == nil {
		s = &settings{}
	}
	schema := &hcl.BodySchema{}
	for _, name := range s.arguments {
		schema.Attributes = append(schema.Attributes, hcl.AttributeSchema{Name: name})
	}
	for _, name := range slices.Sorted(maps.Keys(s.blocks)) {
		schema.Blocks = append(schema.Blocks, hcl.BlockHeaderSchema{Type: name})
	}
	content, diags := body.Content(schema)
	for _, d := range diags {
		if d.Severity == hcl.DiagError && d.Subject != nil {
			return fmt.Errorf("%s: %s sets what infraharvest doesn't generate it with, which could have it run a program or send its requests elsewhere: %s It takes only %s", where(*d.Subject), what, d.Detail, s)
		}
	}
	if diags.HasErrors() {
		return diags
	}
	for _, b := range content.Blocks {
		if err := checkSettings(b.Body, s.blocks[b.Type], what+"'s "+b.Type); err != nil {
			return err
		}
	}
	return nil
}

// String lists what s takes, for a message: its arguments, and each
// nested block with what it takes in braces.
func (s *settings) String() string {
	items := slices.Clone(s.arguments)
	for name, nested := range s.blocks {
		if len(nested.arguments) == 0 && len(nested.blocks) == 0 {
			items = append(items, name+" {}")
		} else {
			items = append(items, name+" { "+nested.String()+" }")
		}
	}
	slices.Sort(items)
	if len(items) == 0 {
		return "nothing"
	}
	return strings.Join(items, ", ")
}

// rootOfBackendConfig returns the directory of the root path is the
// backend settings of an environment of, vars/<env>/backend.hcl in a root
// environments share, which drift initialises the backend with; "" for
// any other path.
func rootOfBackendConfig(path string) string {
	vars := filepath.Dir(filepath.Dir(path))
	if filepath.Base(path) != "backend.hcl" || filepath.Base(vars) != "vars" {
		return ""
	}
	return filepath.Dir(vars)
}

// bodies returns the bodies of files.
func bodies(files []*hcl.File) []hcl.Body {
	b := make([]hcl.Body, len(files))
	for i, f := range files {
		b[i] = f.Body
	}
	return b
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
			u.providers = append(u.providers, b)
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
// constraint alone; and its backend, which checkModuleDir checks. A
// cloud block, which sends a token to the host it names, is refused:
// infraharvest doesn't generate one, and drift doesn't plan with it.
func (u *moduleUses) readTerraform(body hcl.Body) error {
	content, _, diags := body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "required_providers"},
		{Type: "backend", LabelNames: []string{"type"}},
		{Type: "cloud"},
	}})
	if diags.HasErrors() {
		return diags
	}
	for _, b := range content.Blocks {
		switch b.Type {
		case "cloud":
			return fmt.Errorf("%s: a cloud block, which infraharvest doesn't generate roots with", where(b.DefRange))
		case "backend":
			u.backends = append(u.backends, b)
			continue
		}
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
