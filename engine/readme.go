// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"sort"
	"strings"
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
	b.WriteString("# Imported resources\n\n")
	fmt.Fprintf(&b, "infraharvest generated this configuration for %d existing %s:\n\n", total, plural(total, "resource", "resources"))
	b.WriteString("| Type | Count |\n|---|---|\n")
	for _, t := range names {
		fmt.Fprintf(&b, "| `%s` | %d |\n", t, types[t])
	}

	b.WriteString("\n## Next steps\n\n")
	step := 1
	if len(result.Secrets) > 0 {
		fmt.Fprintf(&b, "%d. Set the secret variables in `%s`. Terraform doesn't write secret values into the configuration it generates. Keep the values out of version control, for example in a `.tfvars` file (`.gitignore` excludes them):\n\n", step, VariablesFileName)
		for _, s := range result.Secrets {
			fmt.Fprintf(&b, "   - `%s`: %s of `%s`\n", s.Variable, s.Attribute, s.Address)
		}
		b.WriteString("\n")
		step++
	}
	fmt.Fprintf(&b, "%d. Run `terraform init` and `terraform plan`. Every resource should be planned for import, with no changes.\n", step)
	fmt.Fprintf(&b, "%d. Run `terraform apply` to record the resources in state. It changes nothing in the cloud.\n", step+1)
	fmt.Fprintf(&b, "%d. Delete `%s`: the import blocks have done their job.\n", step+2, ImportsFileName)

	if len(result.Rejected) > 0 {
		fmt.Fprintf(&b, "\n## Left out\n\nTerraform couldn't import these resources, or generate valid configuration for them. `%s` has their blocks and errors: fix them and move the blocks into `%s` and `%s`, or leave the resources out.\n\n", RejectedFileName, ImportsFileName, GeneratedFileName)
		for _, r := range result.Rejected {
			fmt.Fprintf(&b, "- `%s`\n", r.Address)
		}
	}
	if len(result.Modules) > 0 {
		b.WriteString(modulesSection(result.Modules))
	}
	if len(result.Gate) > 0 {
		b.WriteString("\n## Checks\n\ninfraharvest checked this directory after generating it. Secret variables had placeholder values in the plan.\n\n| Check | Result |\n|---|---|\n")
		for _, c := range result.Gate {
			status := "passed"
			if !c.Passed {
				status = "**failed**"
			}
			fmt.Fprintf(&b, "| %s | %s |\n", c.Name, status)
		}
		var details []string
		for _, c := range result.Gate {
			for _, d := range c.Details {
				details = append(details, fmt.Sprintf("- %s: %s", c.Name, d))
			}
		}
		if len(details) > 0 {
			fmt.Fprintf(&b, "\n%s\n", strings.Join(details, "\n"))
		}
	}
	return []byte(b.String())
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
