// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package moduleinterface reads a registry module version's interface (its
// variables and outputs) and checks adapters against it: the snapshots in
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

// Interface is a module version's variables and outputs.
type Interface struct {
	Source    string              `json:"source"`
	Version   string              `json:"version"`
	Variables map[string]Variable `json:"variables"`
	Outputs   []string            `json:"outputs"`
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

// Read reads the interface of a module from a tar.gz of its repository:
// the .tf files of subdir, such as modules/iam-role, in the archive's one
// top-level directory, or of that directory itself if subdir is "".
func Read(r io.Reader, subdir string) (*Interface, error) {
	subdir = strings.Trim(subdir, "/")
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	iface := &Interface{Variables: map[string]Variable{}}
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
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(name, ".tf") || top == "" || rest != subdir {
			continue
		}
		src, err := io.ReadAll(io.LimitReader(tr, maxRead+1))
		if err != nil {
			return nil, err
		}
		if len(src) > maxRead {
			return nil, fmt.Errorf("%s: larger than %d bytes", h.Name, maxRead)
		}
		f, diags := hclsyntax.ParseConfig(src, h.Name, hcl.InitialPos)
		if diags.HasErrors() {
			return nil, fmt.Errorf("%s: %w", h.Name, diags)
		}
		for _, b := range f.Body.(*hclsyntax.Body).Blocks {
			switch {
			case b.Type == "variable" && len(b.Labels) == 1:
				iface.Variables[b.Labels[0]] = readVariable(b.Body, src)
			case b.Type == "output" && len(b.Labels) == 1:
				iface.Outputs = append(iface.Outputs, b.Labels[0])
			}
		}
	}
	sort.Strings(iface.Outputs)
	if len(iface.Variables) == 0 {
		return nil, errors.New("no variables in the module")
	}
	return iface, nil
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
// outputs the module doesn't have, and defaults (see Adapter.Defaults)
// that aren't the ones the interface records.
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
