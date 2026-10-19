// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package moduleinterface reads a registry module version's interface (its
// variables and outputs, the providers it requires and the resources it
// declares) and checks adapters against it: the snapshots in
// internal/adapters/testdata/interfaces, and the nightly check for newer versions.
package moduleinterface

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// RegistryURL is the public Terraform registry.
const RegistryURL = "https://registry.terraform.io"

// maxRead bounds what is read of a registry answer and of each .tf file
// of a module archive, well above any real one.
const maxRead = 16 << 20

// maxModule bounds the .tf files of a module archive read in all.
const maxModule = 64 << 20

// Interface is a module version's variables and outputs, the providers it
// requires and the resources it declares.
type Interface struct {
	Source    string              `json:"source"`
	Version   string              `json:"version"`
	Variables map[string]Variable `json:"variables"`
	Outputs   []string            `json:"outputs"`
	// Providers are the providers the module requires, by the local name
	// its terraform block's required_providers gives each. Resources are
	// the addresses of the resources it declares, type.name, with those
	// of the local modules it calls under module.<call>., such as
	// module.vpc.google_compute_network.network. An interface read before
	// they were recorded has neither.
	Providers map[string]Provider `json:"providers,omitempty"`
	Resources []string            `json:"resources,omitempty"`
}

// Provider is a provider a module requires: its source, if the module
// names one, and its version constraint as written.
type Provider struct {
	Source  string `json:"source,omitempty"`
	Version string `json:"version,omitempty"`
}

// Variable is a module variable: whether it is required, its type
// constraint as written (white space collapsed), and its default as JSON,
// when the default is a value without references or function calls. An
// interface read before types and defaults were recorded has neither.
type Variable struct {
	Required bool            `json:"required"`
	Type     string          `json:"type,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
}

// Client reads modules from a registry.
type Client struct {
	HTTP     *http.Client
	Registry string // such as RegistryURL
	// Archive returns the URL of a tar.gz of a GitHub repository at ref;
	// codeload.github.com by default.
	Archive func(owner, repo, ref string) string
}

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.HTTP.Do(req)
}

// SplitSource splits a module source into the registry module and the
// directory of a submodule in it, if any:
// terraform-aws-modules/iam/aws//modules/iam-role is the iam module's
// modules/iam-role.
func SplitSource(source string) (module, subdir string) {
	module, subdir, _ = strings.Cut(source, "//")
	return module, subdir
}

// Latest returns the newest release of a module (no pre-releases).
func (c *Client) Latest(ctx context.Context, source string) (string, error) {
	module, _ := SplitSource(source)
	resp, err := c.get(ctx, c.Registry+"/v1/modules/"+module+"/versions")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s versions: %s", source, resp.Status)
	}
	var body struct {
		Modules []struct {
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRead)).Decode(&body); err != nil {
		return "", fmt.Errorf("%s versions: %w", source, err)
	}
	var newest *version.Version
	for _, m := range body.Modules {
		for _, v := range m.Versions {
			parsed, err := version.NewVersion(v.Version)
			if err != nil || parsed.Prerelease() != "" {
				continue
			}
			if newest == nil || parsed.GreaterThan(newest) {
				newest = parsed
			}
		}
	}
	if newest == nil {
		return "", fmt.Errorf("%s has no releases", source)
	}
	return newest.Original(), nil
}

// Fetch reads a module version's interface from the .tf files of its root
// module, or of the submodule source names (see SplitSource). Only modules
// hosted on GitHub are supported.
func (c *Client) Fetch(ctx context.Context, source, ver string) (*Interface, error) {
	module, subdir := SplitSource(source)
	resp, err := c.get(ctx, c.Registry+"/v1/modules/"+module+"/"+ver+"/download")
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	location := resp.Header.Get("X-Terraform-Get")
	if location == "" {
		return nil, fmt.Errorf("%s %s download: %s, no X-Terraform-Get", source, ver, resp.Status)
	}
	owner, repo, ref, err := githubSource(location)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", source, ver, err)
	}
	archive := c.Archive
	if archive == nil {
		archive = func(owner, repo, ref string) string {
			return "https://codeload.github.com/" + owner + "/" + repo + "/tar.gz/" + ref
		}
	}
	resp, err = c.get(ctx, archive(owner, repo, ref))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s archive: %s", source, ver, resp.Status)
	}
	iface, err := Read(resp.Body, subdir)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", source, ver, err)
	}
	iface.Source, iface.Version = source, ver
	return iface, nil
}

// githubSource parses git::https://github.com/<owner>/<repo>?ref=<ref>.
func githubSource(location string) (owner, repo, ref string, err error) {
	u, err := url.Parse(strings.TrimPrefix(location, "git::"))
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	ref = u.Query().Get("ref")
	if u.Host != "github.com" || len(parts) != 2 || ref == "" {
		return "", "", "", fmt.Errorf("not a GitHub source with a ref: %s", location)
	}
	return parts[0], parts[1], ref, nil
}

// tfFile is a .tf file of a module archive.
type tfFile struct {
	name string
	src  []byte
}

// Read reads the interface of a module from a tar.gz of its repository:
// the .tf files of subdir, such as modules/iam-role, in the archive's one
// top-level directory, or of that directory itself if subdir is "", and
// for its resources those of the local modules it calls.
func Read(r io.Reader, subdir string) (*Interface, error) {
	subdir = strings.Trim(subdir, "/")
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	// The .tf files by directory, relative to the top-level one: only
	// the module's and those of the local modules it calls are parsed.
	files := map[string][]tfFile{}
	total := 0
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		dir, name := path.Split(strings.TrimPrefix(h.Name, "./"))
		top, rest, _ := strings.Cut(strings.Trim(dir, "/"), "/")
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(name, ".tf") || top == "" {
			continue
		}
		src, err := io.ReadAll(io.LimitReader(tr, maxRead+1))
		if err != nil {
			return nil, err
		}
		if len(src) > maxRead {
			return nil, fmt.Errorf("%s: larger than %d bytes", h.Name, maxRead)
		}
		if total += len(src); total > maxModule {
			return nil, fmt.Errorf("the archive's .tf files are larger than %d bytes", maxModule)
		}
		files[rest] = append(files[rest], tfFile{name: h.Name, src: src})
	}
	iface := &Interface{Variables: map[string]Variable{}}
	resources := map[string]bool{}
	if err := readModule(files, subdir, "", iface, resources, map[string]bool{}); err != nil {
		return nil, err
	}
	sort.Strings(iface.Outputs)
	if len(resources) > 0 {
		iface.Resources = slices.Sorted(maps.Keys(resources))
	}
	if len(iface.Variables) == 0 {
		return nil, errors.New("no variables in the module")
	}
	return iface, nil
}

// readModule reads the module in dir, of files: its interface into iface,
// unless iface is nil, and the addresses of its resources, and of those
// of the local modules it calls, into resources, under prefix. stack has
// the directories being read, so that a module calling itself ends.
func readModule(files map[string][]tfFile, dir, prefix string, iface *Interface, resources, stack map[string]bool) error {
	if stack[dir] {
		return nil
	}
	stack[dir] = true
	defer delete(stack, dir)
	for _, file := range files[dir] {
		f, diags := hclsyntax.ParseConfig(file.src, file.name, hcl.InitialPos)
		if diags.HasErrors() {
			return fmt.Errorf("%s: %w", file.name, diags)
		}
		for _, b := range f.Body.(*hclsyntax.Body).Blocks {
			switch {
			case b.Type == "resource" && len(b.Labels) == 2:
				resources[prefix+b.Labels[0]+"."+b.Labels[1]] = true
			case b.Type == "module" && len(b.Labels) == 1:
				if called, ok := localSource(dir, b.Body); ok {
					if err := readModule(files, called, prefix+"module."+b.Labels[0]+".", nil, resources, stack); err != nil {
						return err
					}
				}
			case iface == nil:
			case b.Type == "variable" && len(b.Labels) == 1:
				iface.Variables[b.Labels[0]] = readVariable(b.Body, file.src)
			case b.Type == "output" && len(b.Labels) == 1:
				iface.Outputs = append(iface.Outputs, b.Labels[0])
			case b.Type == "terraform":
				readRequiredProviders(b.Body, iface)
			}
		}
	}
	return nil
}

// localSource returns the directory a module call in dir calls, if its
// source is a local path, such as ./modules/vpc.
func localSource(dir string, body *hclsyntax.Body) (string, bool) {
	attr, ok := body.Attributes["source"]
	if !ok {
		return "", false
	}
	source, ok := stringValue(attr.Expr)
	if !ok || !strings.HasPrefix(source, "./") && !strings.HasPrefix(source, "../") {
		return "", false
	}
	return path.Join(dir, source), true
}

// readRequiredProviders reads the required_providers of a terraform block
// into iface: each provider's source and version, as plain strings, or a
// version alone in the old form, a string.
func readRequiredProviders(body *hclsyntax.Body, iface *Interface) {
	for _, b := range body.Blocks {
		if b.Type != "required_providers" {
			continue
		}
		for name, attr := range b.Body.Attributes {
			var p Provider
			pairs, diags := hcl.ExprMap(attr.Expr)
			if diags.HasErrors() {
				p.Version, _ = stringValue(attr.Expr)
			}
			for _, kv := range pairs {
				key, ok := stringValue(kv.Key)
				if !ok {
					continue
				}
				// configuration_aliases refers to providers: not a value.
				value, ok := stringValue(kv.Value)
				switch {
				case !ok:
				case key == "source":
					p.Source = value
				case key == "version":
					p.Version = value
				}
			}
			if iface.Providers == nil {
				iface.Providers = map[string]Provider{}
			}
			iface.Providers[name] = p
		}
	}
}

// stringValue returns the value of an expression that is a string without
// references or function calls.
func stringValue(expr hcl.Expression) (string, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

// readVariable reads a variable block, from the file src.
func readVariable(body *hclsyntax.Body, src []byte) Variable {
	v := Variable{Required: true}
	if attr, ok := body.Attributes["type"]; ok {
		v.Type = strings.Join(strings.Fields(string(attr.Expr.Range().SliceBytes(src))), " ")
	}
	attr, ok := body.Attributes["default"]
	if !ok {
		return v
	}
	v.Required = false
	if len(attr.Expr.Variables()) > 0 {
		return v
	}
	value, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || value.IsNull() || !value.IsWhollyKnown() {
		return v
	}
	if raw, err := ctyjson.Marshal(value, value.Type()); err == nil {
		v.Default = raw
	}
	return v
}

// Check returns how an adapter doesn't fit a module interface: inputs the
// module has no variable for, required variables the adapter never sets,
// outputs the module doesn't have, defaults (see Adapter.Defaults) that
// aren't the ones the interface records, and Inverse entries (see
// Adapter.Inverse) of no resource in the adapter's Targets, or onto an
// input path that doesn't parse or whose variable the module doesn't
// have.
func Check(a adapters.Adapter, iface *Interface) []string {
	var problems []string
	for _, input := range a.Inputs {
		if _, ok := iface.Variables[input]; !ok {
			problems = append(problems, fmt.Sprintf("no variable %q", input))
		}
	}
	names := make([]string, 0, len(iface.Variables))
	for name := range iface.Variables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if iface.Variables[name].Required && !slices.Contains(a.Inputs, name) {
			problems = append(problems, fmt.Sprintf("the adapter doesn't set the required variable %q", name))
		}
	}
	for _, output := range a.Outputs {
		if !slices.Contains(iface.Outputs, output) {
			problems = append(problems, fmt.Sprintf("no output %q", output))
		}
	}
	// A call leaves out what equals an adapter's default: a default the
	// module doesn't have would leave out a setting it needs.
	for _, input := range slices.Sorted(maps.Keys(a.Defaults)) {
		if v, ok := iface.Variables[input]; ok && v.Default != nil && !sameDefault(a.Defaults[input], v.Default) {
			problems = append(problems, fmt.Sprintf("the adapter's default for %q isn't the module's, %s", input, v.Default))
		}
	}
	// Drift takes a change to a resource of the module into the input an
	// entry names: one the module doesn't have would edit nothing it reads.
	for _, key := range slices.Sorted(maps.Keys(a.Inverse)) {
		if _, _, ok := a.InverseTarget(key); !ok {
			problems = append(problems, fmt.Sprintf("Inverse has %q, an attribute of none of the adapter's Targets", key))
		}
		in, err := adapters.ParseInputPath(a.Inverse[key])
		if err != nil {
			problems = append(problems, fmt.Sprintf("Inverse maps %q onto an input path that doesn't parse: %v", key, err))
			continue
		}
		if _, ok := iface.Variables[in.Variable]; !ok {
			problems = append(problems, fmt.Sprintf("Inverse maps %q onto %q, but the module has no variable %q", key, a.Inverse[key], in.Variable))
		}
	}
	return problems
}

// sameDefault reports whether want, an adapter's default, is the default
// recorded as raw, converted to want's type as Adapter.IsDefault converts
// what a call sets.
func sameDefault(want cty.Value, raw json.RawMessage) bool {
	ty, err := ctyjson.ImpliedType(raw)
	if err != nil {
		return false
	}
	v, err := ctyjson.Unmarshal(raw, ty)
	if err != nil {
		return false
	}
	converted, err := convert.Convert(v, want.Type())
	if err != nil {
		return false
	}
	equal := converted.Equals(want)
	return equal.IsKnown() && equal.True()
}

// TypeChanges returns the inputs whose variable's type constraint differs
// between two interfaces of a module, such as a pinned version's and the
// newest release's: an adapter can fit both by name and still set a value
// the newer one no longer takes. Variables without a recorded type in
// either are left out.
func TypeChanges(pinned, newer *Interface, inputs []string) []string {
	var changes []string
	for _, input := range slices.Sorted(slices.Values(inputs)) {
		old, ok := pinned.Variables[input]
		now, found := newer.Variables[input]
		if ok && found && old.Type != "" && now.Type != "" && old.Type != now.Type {
			changes = append(changes, fmt.Sprintf("%q is %s, was %s", input, now.Type, old.Type))
		}
	}
	return changes
}

// NewestRelease returns the newest release of the provider at source that
// every one of constraints accepts, or the newest of all with none, as
// engine.ProviderVersion, which pins the root's provider, does.
type NewestRelease func(source string, constraints []version.Constraints) (*version.Version, error)

// CheckProviders returns, of the providers named (by the local names the
// module's required_providers gives them), those the module accepts no
// release of in the provider's newest major release line, with its
// constraint: an import doesn't call a module that would hold the root a
// major release behind. A provider the module doesn't require, or
// requires without a version, accepts every release, as Terraform takes
// it. The error is a provider whose releases couldn't be looked up.
func CheckProviders(iface *Interface, names []string, newest NewestRelease) ([]string, error) {
	var held []string
	for _, name := range names {
		p, ok := iface.Providers[name]
		if !ok || strings.TrimSpace(p.Version) == "" {
			continue
		}
		c, err := version.NewConstraint(p.Version)
		if err != nil {
			held = append(held, fmt.Sprintf("its constraint on %s, `%s`, doesn't parse", name, p.Version))
			continue
		}
		source := p.Source
		if source == "" {
			source = "hashicorp/" + name
		}
		latest, err := newest(source, nil)
		if err != nil {
			return held, err
		}
		accepted, err := newest(source, []version.Constraints{c})
		if err != nil || accepted.Segments()[0] != latest.Segments()[0] {
			held = append(held, fmt.Sprintf("it accepts %s `%s` only, not %d.x, the newest major release (%s)", name, p.Version, latest.Segments()[0], latest))
		}
	}
	return held, nil
}

// CheckResources returns the targets (see Adapter.Targets) a module
// interface has no resource at, each with the resources of its type, or
// else of its name, in the same module, that it may have become: such as
// an azapi_resource where an azurerm resource was. An interface that
// records no resources isn't checked.
func CheckResources(targets []string, iface *Interface) []string {
	if len(iface.Resources) == 0 {
		return nil
	}
	var problems []string
	for _, target := range targets {
		if slices.Contains(iface.Resources, target) {
			continue
		}
		module, typ, name := splitAddress(target)
		var sameType, sameName []string
		for _, r := range iface.Resources {
			m, t, n := splitAddress(r)
			switch {
			case m != module:
			case t == typ:
				sameType = append(sameType, r)
			case n == name:
				sameName = append(sameName, r)
			}
		}
		problem := fmt.Sprintf("no resource %s, which the adapter maps onto", target)
		instead := sameType
		if len(instead) == 0 {
			instead = sameName
		}
		if len(instead) > 0 {
			problem += fmt.Sprintf(" (the module has %s)", strings.Join(instead, ", "))
		}
		problems = append(problems, problem)
	}
	return problems
}

// splitAddress splits a resource address, such as
// module.vpc.google_compute_network.network, into its module path
// (module.vpc.), its type and its name.
func splitAddress(address string) (module, typ, name string) {
	i := strings.LastIndex(address, ".")
	if i < 0 {
		return "", address, ""
	}
	j := strings.LastIndex(address[:i], ".")
	return address[:j+1], address[j+1 : i], address[i+1:]
}
