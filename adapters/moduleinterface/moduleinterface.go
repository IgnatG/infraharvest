// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package moduleinterface reads a registry module version's interface (its
// variables and outputs) and checks adapters against it: the snapshots in
// adapters/testdata/interfaces, and the nightly check for newer versions.
package moduleinterface

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/adapters"
)

// RegistryURL is the public Terraform registry.
const RegistryURL = "https://registry.terraform.io"

// Interface is a module version's variables and outputs.
type Interface struct {
	Source    string              `json:"source"`
	Version   string              `json:"version"`
	Variables map[string]Variable `json:"variables"`
	Outputs   []string            `json:"outputs"`
}

// Variable is a module variable.
type Variable struct {
	Required bool `json:"required"`
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

// Latest returns the newest release of a module (no pre-releases).
func (c *Client) Latest(ctx context.Context, source string) (string, error) {
	resp, err := c.get(ctx, c.Registry+"/v1/modules/"+source+"/versions")
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
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
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

// Fetch reads a module version's interface from its root module's .tf
// files. Only modules hosted on GitHub are supported.
func (c *Client) Fetch(ctx context.Context, source, ver string) (*Interface, error) {
	resp, err := c.get(ctx, c.Registry+"/v1/modules/"+source+"/"+ver+"/download")
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
	iface, err := Read(resp.Body)
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

// Read reads the interface of the root module in a tar.gz of a module's
// repository: the .tf files at the top level of its one directory.
func Read(r io.Reader) (*Interface, error) {
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
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(name, ".tf") || strings.Count(strings.Trim(dir, "/"), "/") != 0 || dir == "" {
			continue
		}
		src, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		f, diags := hclsyntax.ParseConfig(src, h.Name, hcl.InitialPos)
		if diags.HasErrors() {
			return nil, fmt.Errorf("%s: %w", h.Name, diags)
		}
		for _, b := range f.Body.(*hclsyntax.Body).Blocks {
			switch {
			case b.Type == "variable" && len(b.Labels) == 1:
				_, hasDefault := b.Body.Attributes["default"]
				iface.Variables[b.Labels[0]] = Variable{Required: !hasDefault}
			case b.Type == "output" && len(b.Labels) == 1:
				iface.Outputs = append(iface.Outputs, b.Labels[0])
			}
		}
	}
	sort.Strings(iface.Outputs)
	if len(iface.Variables) == 0 {
		return nil, errors.New("no variables in the module's root")
	}
	return iface, nil
}

// Check returns how an adapter doesn't fit a module interface: inputs the
// module has no variable for, required variables the adapter never sets,
// and outputs the module doesn't have.
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
	return problems
}
