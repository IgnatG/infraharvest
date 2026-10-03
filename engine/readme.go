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

// GitignoreFile keeps state, plans, Terraform's working directory and
// variable values out of version control. Variable files can hold the
// secret values variables.tf asks for.
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
`

// readmeFile describes a directory Generate wrote: what it imports, the
// secret variables to set, what was left out, and the steps to take it
// under management.
func readmeFile(imports []Import, result *Result) []byte {
	rejected := map[string]bool{}
	for _, r := range result.Rejected {
		rejected[r.Address] = true
	}
	types := map[string]int{}
	total := 0
	for _, imp := range imports {
		if !rejected[imp.Type+"."+imp.Name] {
			types[imp.Type]++
			total++
		}
	}
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
	return []byte(b.String())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
