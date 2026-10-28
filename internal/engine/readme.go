// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// ReadmeFileName explains a generated directory and what is left to do.
const ReadmeFileName = "README.md"

// GitignoreFile keeps state, plans, Terraform's working directory, variable
// values and infraharvest's checkpoints (.infraharvest) out of version
// control. Variable files can hold the secret values variables.tf asks for.
const GitignoreFile = `.terraform/
*.tfstate
*.tfstate.*
*.tfplan
crash.log
crash.*.log
*.tfvars
*.tfvars.json
override.tf
override.tf.json
*_override.tf
*_override.tf.json
.infraharvest/
`

// readmeFile describes a directory Generate wrote: what it imports, the
// secret variables to set, what was left out, and the steps to take it
// under management.
func readmeFile(result *Result) []byte {
	types := map[string]int{}
	for _, imp := range result.Imported {
		types[imp.Type]++
	}
	total := len(result.Imported)
	names := make([]string, 0, len(types))
	for t := range types {
		names = append(names, t)
	}
	sort.Strings(names)

	var b strings.Builder
	if total == 0 && len(result.Removed) > 0 {
		return handedOverReadme(result)
	}
	b.WriteString("# Imported resources\n\n")
	fmt.Fprintf(&b, "infraharvest generated this configuration for %d existing %s:\n\n", total, plural(total, "resource", "resources"))
	b.WriteString("| Type | Count |\n|---|---|\n")
	for _, t := range names {
		fmt.Fprintf(&b, "| `%s` | %d |\n", t, types[t])
	}

	b.WriteString("\n## Next steps\n\n")
	step := 1
	if len(result.Secrets) > 0 {
		fmt.Fprintf(&b, "%d. Set the secret variables in `%s`. infraharvest never writes secret values into the configuration: set each from your secret store, such as AWS Secrets Manager, Azure Key Vault or Google Secret Manager, or with `TF_VAR_<name>`, and keep the values out of version control (`.gitignore` excludes `.tfvars` files):\n\n", step, hclfile.VariablesFileName)
		for _, s := range result.Secrets {
			fmt.Fprintf(&b, "   - `%s`: %s of `%s`\n", s.Variable, s.Attribute, s.Address)
		}
		b.WriteString("\n")
		step++
	}
	planned := "Every resource should be planned for import,"
	removes := len(result.Removed) > 0 || len(result.Retyped) > 0
	switch {
	case len(result.Moves) > 0 && removes:
		planned = fmt.Sprintf("Every resource should be planned for import, or as moved for those `%s` moves, or as forgotten, not destroyed, for those it removes (see Moves),", MovesFileName)
	case len(result.Moves) > 0:
		planned = fmt.Sprintf("Every resource should be planned for import, or as moved for those `%s` moves (see Moves),", MovesFileName)
	case removes:
		planned = fmt.Sprintf("Every resource should be planned for import, or as forgotten, not destroyed, for those `%s` removes (see Moves),", MovesFileName)
	}
	if stateOnlyUpdates(result.Gate) {
		// Such as Google Cloud labels, which an import leaves out of state.
		fmt.Fprintf(&b, "%d. Run `terraform init` and `terraform plan`. %s with no changes but the updates the checks below list: to arguments the provider keeps only in state, which the import leaves unset.\n", step, planned)
	} else {
		fmt.Fprintf(&b, "%d. Run `terraform init` and `terraform plan`. %s with no changes.\n", step, planned)
	}
	apply := "Run `terraform apply` to record the resources in state. It changes nothing in the cloud."
	switch {
	case len(result.Removed) > 0 && len(result.Arrived) > 0:
		apply = "Run `terraform apply` to record the resources in state, after applying the roots the resources it imports come from, and before applying the roots those it removes go to (see Moves). It changes nothing in the cloud."
	case len(result.Removed) > 0:
		apply = "Run `terraform apply` to record the resources in state, before applying the roots those it removes go to (see Moves). It changes nothing in the cloud."
	case len(result.Arrived) > 0:
		apply = "Run `terraform apply` to record the resources in state, after applying the roots the resources it imports from other roots come from (see Moves). It changes nothing in the cloud."
	}
	fmt.Fprintf(&b, "%d. %s\n", step+1, apply)
	if len(result.Moves) > 0 || removes {
		fmt.Fprintf(&b, "%d. Delete `%s` and `%s`: the import, moved and removed blocks have done their job.\n", step+2, ImportsFileName, MovesFileName)
	} else {
		fmt.Fprintf(&b, "%d. Delete `%s`: the import blocks have done their job.\n", step+2, ImportsFileName)
	}

	if len(result.Rejected) > 0 {
		fmt.Fprintf(&b, "\n## Left out\n\nTerraform couldn't import these resources, or generate valid configuration for them. `%s` has their blocks and errors: fix them and move the import blocks into `%s` and the resources into the configuration, or leave the resources out.\n\n", RejectedFileName, ImportsFileName)
		for _, r := range result.Rejected {
			fmt.Fprintf(&b, "- `%s`\n", r.Address)
		}
	}
	b.WriteString(movesSection(result))
	if len(result.NotInCloud) > 0 {
		b.WriteString(notInCloudSection(result.NotInCloud))
	}
	if len(result.Modules) > 0 {
		b.WriteString(modulesSection(result.Modules))
	}
	if len(result.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, w := range result.Warnings {
			fmt.Fprintf(&b, "- %s.\n", w)
		}
	}
	b.WriteString(checksSection(result.Gate))
	return []byte(b.String())
}

// handedOverReadme describes a root whose resources all go to other roots
// now (see Vacate): what is left to do is forget them.
func handedOverReadme(result *Result) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Handed over resources\n\ninfraharvest generated this root before. Its resources now go to other roots of the output, which import them, so it keeps only what Terraform needs to forget them: `%s`, `%s` and `%s`.\n", VersionsFileName, ProvidersFileName, MovesFileName)
	b.WriteString("\n## Next steps\n\n")
	fmt.Fprintf(&b, "1. Run `terraform init` and `terraform plan`. Every resource the removed blocks in `%s` remove should be planned as forgotten, not destroyed, with no other changes.\n", MovesFileName)
	b.WriteString("2. Run `terraform apply`, before applying the roots the resources go to (see Moves). It removes them from this root's state, and changes nothing in the cloud.\n")
	fmt.Fprintf(&b, "3. Delete `%s`: the removed blocks have done their job, and the root manages nothing.\n", MovesFileName)
	b.WriteString(movesSection(result))
	b.WriteString(checksSection(result.Gate))
	return []byte(b.String())
}

// checksSection lists the verification gate's checks, with their details.
func checksSection(checks gate.Gate) string {
	var b strings.Builder
	if len(checks) > 0 {
		b.WriteString("\n## Checks\n\ninfraharvest checked this directory after generating it. Secret variables had their values in the plan where infraharvest found them in the configuration, and placeholder values where Terraform doesn't show them.\n\n| Check | Result |\n|---|---|\n")
		for _, c := range checks {
			status := "passed"
			switch {
			case !c.Passed:
				status = "**failed**"
			case c.Skipped:
				status = "skipped"
			}
			fmt.Fprintf(&b, "| %s | %s |\n", c.Name, status)
		}
		var details []string
		for _, c := range checks {
			for _, d := range c.Details {
				details = append(details, fmt.Sprintf("- %s: %s", c.Name, d))
			}
		}
		if len(details) > 0 {
			fmt.Fprintf(&b, "\n%s\n", strings.Join(details, "\n"))
		}
	}
	return b.String()
}

// stateOnlyUpdates reports whether the plan check passed with resources
// that update state-only arguments (see gate.PlanCheck).
func stateOnlyUpdates(checks gate.Gate) bool {
	c := checks.Named(gate.CheckPlan)
	return c.Passed && slices.ContainsFunc(c.Details, func(d string) bool { return strings.Contains(d, gate.StateOnlyChange) })
}

// modulesSection lists the module calls and the clusters of resources that
// stayed in the root, with why.
func modulesSection(calls []ModuleCall) string {
	var b strings.Builder
	b.WriteString("\n## Modules\n\n")
	var declined []ModuleCall
	made := 0
	for _, c := range calls {
		if c.Declined != "" {
			declined = append(declined, c)
			continue
		}
		if made == 0 {
			b.WriteString("These resources are managed through modules. infraharvest kept each module call only because the plan was the same as with the resources in the root.\n\n| Call | Module | Resources |\n|---|---|---|\n")
		}
		made++
		source := "`" + c.Source + "`"
		if c.Version != "" {
			source += " " + c.Version
		}
		fmt.Fprintf(&b, "| `module.%s` | %s | %s |\n", c.Name, source, codeList(c.Resources))
	}
	if len(declined) > 0 {
		if made > 0 {
			b.WriteString("\n")
		}
		b.WriteString("These resources stay in the root, as a module couldn't manage them the same way:\n\n")
		for _, c := range declined {
			fmt.Fprintf(&b, "- %s (`%s` %s): %s\n", codeList(c.Resources), c.Source, c.Version, c.Declined)
		}
	}
	return b.String()
}

// notInCloudSection lists the resources of the root that the incremental
// import which added to it didn't find in the cloud, where it listed all
// there is, with what to do about them: a plan would create them again,
// and one with its import block still in imports.tf fails, as there is
// nothing to import. infraharvest changes nothing the root has, so the
// blocks stay for people to delete; a removed block wouldn't do, as it
// only forgets what state has, and the resource isn't there to forget.
func notInCloudSection(gone []Import) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## No longer in the cloud\n\nThe incremental import that last added to this root didn't find these resources, though it listed every resource of their type where they were: they have been deleted since. The next plan would create them again, and fails while `%s` still has their import blocks. Delete each one's import block, if `%s` still has it, then either delete its resource block, and anything that refers to it, and plan; or, if it should exist, plan to create it again.\n\n", ImportsFileName, ImportsFileName)
	for _, g := range gone {
		fmt.Fprintf(&b, "- `%s.%s` (`%s`)\n", g.Type, g.Name, g.ID)
	}
	return b.String()
}

// movesSection lists what moves.tf moves: the resources of a root
// generated again at other addresses than before, from where its state
// has them; those of another name of their type now, which its removed
// blocks forget and its import blocks import again; what its removed
// blocks hand over to other roots; and what the root imports that other
// roots hand over, which must be applied first. It is empty if there is
// none of these.
func movesSection(result *Result) string {
	if len(result.Moves) == 0 && len(result.Retyped) == 0 && len(result.Removed) == 0 && len(result.Arrived) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Moves\n")
	if len(result.Moves) > 0 {
		fmt.Fprintf(&b, "\nThese resources have other addresses than when this root was generated before. The moved blocks in `%s` move them in state, so the plan shows each as moved rather than destroyed and created again.\n\n| From | To |\n|---|---|\n", MovesFileName)
		for _, m := range result.Moves {
			fmt.Fprintf(&b, "| `%s` | `%s` |\n", m.From, m.To)
		}
	}
	if len(result.Retyped) > 0 {
		fmt.Fprintf(&b, "\nThese resources are of another name of their type now, which no moved block can follow. The removed blocks in `%s` make this root's state forget them where it had them, without destroying them, and the import blocks in `%s` import them again where they are now. Removed blocks with `destroy = false` need Terraform 1.7 or OpenTofu 1.10, so `%s` requires 1.10 or later while `%s` has them.\n\n| Was | Is now |\n|---|---|\n", MovesFileName, ImportsFileName, VersionsFileName, MovesFileName)
		for _, r := range result.Retyped {
			fmt.Fprintf(&b, "| `%s` | `%s` |\n", r.From, r.To)
		}
	}
	if len(result.Removed) > 0 {
		fmt.Fprintf(&b, "\nThese resources go to other roots of the output now. The removed blocks in `%s` make this root's state forget them, without destroying them, and the other roots import them. Apply this root first, then the root each goes to: until then, both would manage it. Removed blocks with `destroy = false` need Terraform 1.7 or OpenTofu 1.10, so `%s` requires 1.10 or later while `%s` has them.\n\n| Removed | Goes to |\n|---|---|\n", MovesFileName, VersionsFileName, MovesFileName)
		for _, r := range result.Removed {
			var roots []string
			for _, d := range r.Resources {
				if !slices.Contains(roots, d.Root) {
					roots = append(roots, d.Root)
				}
			}
			sort.Strings(roots)
			to := "another root"
			if len(roots) > 0 {
				to = codeList(roots)
			}
			removed := "`" + r.From + "`"
			if r.Instance != "" {
				// A moved block moves it to From first, for the removed block.
				removed = fmt.Sprintf("`%s`, moved to `%s`", r.Instance, r.From)
			}
			fmt.Fprintf(&b, "| %s | %s |\n", removed, to)
		}
	}
	if len(result.Arrived) > 0 {
		b.WriteString("\nThese resources come from other roots of the output, whose removed blocks make their state forget them. Apply the root each comes from first, then this root, which imports it: until then, both would manage it.\n\n| Imported | Comes from | Was |\n|---|---|---|\n")
		for _, a := range result.Arrived {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n", a.To, a.Root, a.From)
		}
	}
	return b.String()
}

func codeList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "`" + s + "`"
	}
	return strings.Join(quoted, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// WriteReadme writes the README of dir, a root that holds result.
func WriteReadme(dir string, result *Result) error {
	return fsutil.WriteFile(filepath.Join(dir, ReadmeFileName), readmeFile(result), 0o644)
}

// checkReadme runs scanSecrets' checks on the README Generate wrote after
// the gate ran, which the gate couldn't see, with values the secret values
// no file may contain. Should the README hold one, G6 fails and the README
// is written again to say so.
func checkReadme(dir string, result *Result, values []string) error {
	content, err := os.ReadFile(filepath.Join(dir, ReadmeFileName))
	if err != nil {
		return err
	}
	found := secret.InFile(secret.NewMatcher(values), ReadmeFileName, string(content))
	if len(found) == 0 {
		return nil
	}
	for i, c := range result.Gate {
		if c.Name == gate.CheckSecrets {
			result.Gate[i].Passed = false
			result.Gate[i].Details = append(result.Gate[i].Details, found...)
		}
	}
	return WriteReadme(dir, result)
}
