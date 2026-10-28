// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// variableReference matches a reference to a variable, its name the first
// group.
var variableReference = regexp.MustCompile(`\bvar\.([A-Za-z_][A-Za-z0-9_-]*)`)

// VacateMerged writes the merged root in dir, which environments share
// (see mergedRootFiles), all of whose resources, in every environment, go
// to other roots now (see Removal), as Vacate does a root of its own. Of
// files, the files its merge wrote, by path in dir with forward slashes,
// it keeps what each environment plans it with: providers.tf, .gitignore
// and each environment's vars/<env>/backend.hcl, and variables.tf and the
// var files with only the variables providers.tf reads. It removes the
// rest, its code, imports.tf and each vars/<env>/imports.tfvars, and
// writes moves.tf with removed, the removed blocks of what it had, its
// required_version raised to what they need, and a README that says to
// apply it as each environment before the roots they go to. Each
// environment's plan of it then forgets them, and destroys nothing. It
// runs no Terraform: it returns the files it leaves, by path in dir with
// forward slashes, sorted, and the moves check of them (see checkMoves).
// It fails if removed is empty, or its moves.tf still moves resources.
func VacateMerged(dir string, files []string, removed []Removal) ([]string, gate.Check, error) {
	if len(removed) == 0 {
		return nil, gate.Check{}, errors.New("no resources to hand over")
	}
	moved, err := ReadMoves(dir)
	if err != nil {
		return nil, gate.Check{}, err
	}
	if len(moved) > 0 {
		return nil, gate.Check{}, fmt.Errorf("%s still moves resources: plan and apply every environment, and delete %s, before generating it again", MovesFileName, MovesFileName)
	}
	envs, err := hclfile.MergedEnvironments(dir)
	if err != nil {
		return nil, gate.Check{}, err
	}
	providers, err := os.ReadFile(filepath.Join(dir, ProvidersFileName))
	if err != nil {
		return nil, gate.Check{}, err
	}
	reads := map[string]bool{}
	for _, m := range variableReference.FindAllSubmatch(providers, -1) {
		reads[string(m[1])] = true
	}
	// What each environment plans the root with is kept: its backend
	// settings, even those of an environment whose values are gone, and
	// the var files of its values, the shared ones even with no
	// environment left, but not those of its import IDs, as the root
	// imports nothing now.
	values := map[string]bool{hclfile.CommonVarsPath: true}
	for _, env := range envs {
		values[hclfile.EnvVarsPath(env)] = true
	}
	var left []string
	for _, name := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		keep := false
		switch {
		case name == ProvidersFileName, name == ".gitignore", name == tfrun.LockFileName, hclfile.IsBackendConfig(name):
			keep = true
		case name == hclfile.VariablesFileName:
			if keep, err = keepVariables(path, reads); err != nil {
				return nil, gate.Check{}, err
			}
		case values[name]:
			if err := keepValues(path, reads); err != nil {
				return nil, gate.Check{}, err
			}
			keep = true
		}
		if keep {
			left = append(left, name)
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, gate.Check{}, err
		}
	}
	if err := writeMoves(dir, nil, removed, nil); err != nil {
		return nil, gate.Check{}, err
	}
	if err := raiseRequiredVersion(dir); err != nil {
		return nil, gate.Check{}, err
	}
	check, err := checkMoves(dir)
	if err != nil {
		return nil, gate.Check{}, err
	}
	backend := slices.ContainsFunc(left, hclfile.IsBackendConfig)
	if err := fsutil.WriteFile(filepath.Join(dir, ReadmeFileName), vacatedMergedReadme(envs, backend, removed, check), 0o644); err != nil {
		return nil, gate.Check{}, err
	}
	left = append(left, MovesFileName, ReadmeFileName)
	slices.Sort(left)
	return slices.Compact(left), check, nil
}

// keepVariables cuts the variables.tf at path down to the variables of
// reads, and removes it if it has none of them. It reports whether it
// keeps it.
func keepVariables(path string, reads map[string]bool) (bool, error) {
	f, err := parseWritable(path)
	if err != nil || f == nil {
		return false, err
	}
	kept := 0
	for _, b := range f.Body().Blocks() {
		if b.Type() == "variable" && len(b.Labels()) == 1 && !reads[b.Labels()[0]] {
			f.Body().RemoveBlock(b)
			continue
		}
		kept++
	}
	if kept == 0 {
		return false, os.Remove(path)
	}
	return true, fsutil.WriteFile(path, formatHCL(string(f.Bytes())), 0o644)
}

// keepValues cuts the var file at path down to the values of reads, the
// variables kept.
func keepValues(path string, reads map[string]bool) error {
	f, err := parseWritable(path)
	if err != nil || f == nil {
		return err
	}
	for name := range f.Body().Attributes() {
		if !reads[name] {
			f.Body().RemoveAttribute(name)
		}
	}
	return fsutil.WriteFile(path, formatHCL(string(f.Bytes())), 0o644)
}

// parseWritable parses the HCL file at path for editing; nil if there is
// none.
func parseWritable(path string) (*hclwrite.File, error) {
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f, diags := hclfile.ParseDiagnostics(path, src)
	if diags.HasErrors() {
		return nil, diags
	}
	return f.Edit(), nil
}

// vacatedMergedReadme describes a merged root of envs, its environments,
// whose resources all go to other roots now (see VacateMerged): what is
// left to do is forget them, in each environment's state, with its
// backend settings, if backend, or else in its workspace.
func vacatedMergedReadme(envs []string, backend bool, removed []Removal, check gate.Check) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Handed over resources\n\ninfraharvest merged this root before, for %s. Its resources, in every environment, now go to other roots of the output, which import them, so it keeps only what Terraform needs to forget them: `%s`, `%s`, and each environment's settings in `%s/<env>/`.\n", strings.Join(envs, ", "), ProvidersFileName, MovesFileName, hclfile.VarsDir)
	b.WriteString("\n## Next steps\n\n")
	env := "<env>"
	if len(envs) > 0 {
		env = envs[0]
	}
	fmt.Fprintf(&b, "1. Plan each environment with its own credentials. For %s, and likewise for the others:\n\n   ```sh\n", env)
	if backend {
		fmt.Fprintf(&b, "   terraform init -reconfigure -backend-config=%s\n", hclfile.BackendConfigPath(env))
	} else {
		fmt.Fprintf(&b, "   terraform init\n   terraform workspace select %s\n", env)
	}
	varFiles := varFileArgs(env, false)
	fmt.Fprintf(&b, "   terraform plan %s\n   ```\n\n   Every resource the removed blocks in `%s` remove should be planned as forgotten, not destroyed, with no other changes.\n", varFiles, MovesFileName)
	fmt.Fprintf(&b, "2. Run `terraform apply %s` as each environment, before applying the roots the resources go to. It removes them from the environment's state, and changes nothing in the cloud.\n", varFiles)
	fmt.Fprintf(&b, "3. Delete `%s` once every environment has been applied: the removed blocks have done their job, and the root manages nothing.\n", MovesFileName)
	fmt.Fprintf(&b, "\n## Handed over to other roots\n\nThese resources go to other roots now, which import them. The removed blocks in `%s`, with `destroy = false`, make each environment's state forget them, without destroying them.\n\n| Resource | Goes to |\n|---|---|\n", MovesFileName)
	for _, r := range removed {
		if len(r.Resources) == 0 {
			fmt.Fprintf(&b, "| `%s` | |\n", r.From)
		}
		for _, d := range r.Resources {
			fmt.Fprintf(&b, "| `%s` | `%s` |\n", d.Address, d.Root)
		}
	}
	b.WriteString(checksSection(gate.Gate{check}))
	return []byte(b.String())
}
