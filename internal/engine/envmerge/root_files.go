// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// localsFile writes locals.tf: naming_prefix and the scope's and region's
// IDs first, then the resources' names, under a comment naming the file
// of their resources.
func (w *rootWriter) localsFile() []byte {
	fileOfLocal := map[string]string{}
	for _, r := range w.m.resources {
		if r.body == nil {
			continue
		}
		for _, a := range r.body.attributes {
			if a.local != "" {
				fileOfLocal[a.local] = w.fileOf[r]
			}
		}
	}
	for _, g := range w.m.forEach {
		if g.local != "" {
			fileOfLocal[g.local] = w.groupFile[g]
		}
	}
	// The resources' names by file, each file's in the model's order.
	locals := slices.Clone(w.m.locals)
	sort.SliceStable(locals, func(i, j int) bool { return fileOfLocal[locals[i].name] < fileOfLocal[locals[j].name] })
	var b strings.Builder
	b.WriteString("locals {\n")
	section := ""
	for i, l := range locals {
		if f := fileOfLocal[l.name]; f != section {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "# %s\n", strings.TrimSuffix(f, ".tf"))
			section = f
		}
		fmt.Fprintf(&b, "%s = %s\n", l.name, exprText(l.value))
	}
	b.WriteString("}\n")
	return formatHCL(b.String())
}

// dataFile writes data.tf: the data sources values read, such as the
// account's ID.
func (w *rootWriter) dataFile() []byte {
	var b strings.Builder
	b.WriteString(rootfile.DataFileHeader)
	written := map[string]bool{}
	for _, d := range w.m.rootData {
		typ, name, _ := strings.Cut(d.address, ".")
		fmt.Fprintf(&b, "\ndata %q %q {\n", typ, name)
		if d.deploy != nil {
			fmt.Fprintf(&b, "count = var.%s ? 1 : 0\n", d.deploy.name)
			if len(d.body.attributes) > 0 {
				b.WriteString("\n")
			}
		}
		writeMergedBody(&b, d.body)
		b.WriteString("}\n")
		written[d.address] = true
	}
	for _, d := range w.m.data {
		if written[d] {
			continue
		}
		typ, name, _ := strings.Cut(d, ".")
		// A root of google-beta reads its data sources through it too, as it
		// requires no google.
		if implied, _, _ := strings.Cut(typ, "_"); implied != w.m.provider {
			fmt.Fprintf(&b, "\ndata %q %q {\n  provider = %s\n}\n", typ, name, w.m.provider)
			continue
		}
		fmt.Fprintf(&b, "\ndata %q %q {}\n", typ, name)
	}
	return formatHCL(b.String())
}

// importsFile writes imports.tf: an import block for each resource of the
// merged root, whose ID, or identity, each environment's imports.tfvars
// gives (see stackMerge.importKinds); for a resource some environments
// lack, or a forEach, with for_each over what the environment has. It returns nil for none.
func (w *rootWriter) importsFile() ([]byte, error) {
	return w.importsFileWithout(nil)
}

// importsFileWithout is importsFile without the import blocks of skip,
// resources of the merged root that aren't of a forEach.
func (w *rootWriter) importsFileWithout(skip map[*stackResource]bool) ([]byte, error) {
	hasID := func(r *stackResource) bool {
		for _, member := range r.members {
			if member != nil && member.id != "" {
				return true
			}
		}
		return false
	}
	kinds, err := w.m.importKinds()
	if err != nil {
		return nil, err
	}
	type block struct{ address, text string }
	var blocks []block
	for _, r := range w.m.resources {
		if r.resourceType != moduleType {
			continue
		}
		calls, err := moduleImports(r, kinds)
		if err != nil {
			return nil, err
		}
		for address, text := range calls {
			blocks = append(blocks, block{address, text})
		}
	}
	for _, r := range w.m.resources {
		if r.forEach != nil || !hasID(r) || skip[r] {
			continue
		}
		instance := string(hclwrite.TokensForTraversal(r.instance()).Bytes())
		kind, err := importKindOf(kinds, r.address(), instance)
		if err != nil {
			return nil, err
		}
		id := kind.value(string(hclwrite.TokensForValue(cty.StringVal(instance)).Bytes()))
		if r.deploy == nil {
			blocks = append(blocks, block{r.address(), fmt.Sprintf("import {\nto = %s\n%s\n}\n", instance, id)})
			continue
		}
		blocks = append(blocks, block{r.address(), fmt.Sprintf("import {\nfor_each = [for k in [0] : k if var.%s]\n\nto = %s.%s[each.value]\n%s\n}\n", r.deploy.name, r.resourceType, r.label, id)})
	}
	for _, g := range w.m.forEach {
		if !slices.ContainsFunc(g.keys, hasID) {
			continue
		}
		instances := make([]string, len(g.keys))
		for i, k := range g.keys {
			instances[i] = string(hclwrite.TokensForTraversal(k.instance()).Bytes())
		}
		kind, err := importKindOf(kinds, g.address(), instances...)
		if err != nil {
			return nil, err
		}
		format := string(hclwrite.TokensForValue(cty.StringVal(g.address() + "[%q]")).Bytes())
		blocks = append(blocks, block{g.address(), fmt.Sprintf("import {\nfor_each = %s\n\nto = %s[each.key]\n%s\n}\n", g.source(), g.address(), kind.value("format("+format+", each.key)"))})
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].address < blocks[j].address })
	var b strings.Builder
	b.WriteString("# Each environment imports its resources on its first apply, with their IDs\n# from " + hclfile.ImportVarsPath("<env>") + ". Delete this file, and those, after it.\n")
	for _, blk := range blocks {
		b.WriteString("\n" + blk.text)
	}
	return formatHCL(b.String()), nil
}

// readme writes the merged root's README.md: its environments, and how to
// plan and apply each.
func (w *rootWriter) readme() []byte {
	var b strings.Builder
	b.WriteString("# " + w.title() + "\n\n")
	fmt.Fprintf(&b, "Generated by infraharvest: one root whose code every environment shares (%s). What differs between them is in `%s/<env>/`: `%s` holds the values that differ from the defaults in `variables.tf`, `%s` the IDs `imports.tf` imports, and `%s` where the state is kept.\n\n", strings.Join(w.m.envs, ", "), hclfile.VarsDir, hclfile.EnvVarsFileName, hclfile.ImportVarsFileName, hclfile.BackendConfigFileName)
	regions := slices.ContainsFunc(w.m.roots, func(r envRoot) bool { return r.region != "" })
	header := "| Environment | `env` | " + w.m.profile.ScopeTitle + " |"
	rule := "|---|---|---|"
	if regions {
		header += " Region |"
		rule += "---|"
	}
	b.WriteString(header + "\n" + rule + "\n")
	for e, root := range w.m.roots {
		fmt.Fprintf(&b, "| %s | `%s` | %s |", w.m.envs[e], w.m.norms[e].token, root.scope)
		if regions {
			fmt.Fprintf(&b, " %s |", root.region)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Plan and apply an environment\n\n")
	env := w.m.envs[0]
	fmt.Fprintf(&b, "Run each environment with its own credentials: the provider block sets no role of its own. For %s, and likewise for the others:\n\n```sh\n", env)
	if w.backendType() != "" {
		fmt.Fprintf(&b, "terraform init -reconfigure -backend-config=%s\n", hclfile.BackendConfigPath(env))
	} else {
		fmt.Fprintf(&b, "terraform init\nterraform workspace select -or-create %s\n", env)
	}
	varFiles := varFileArgs(env, true)
	fmt.Fprintf(&b, "terraform plan %s\nterraform apply %s\n```\n\n", varFiles, varFiles)
	if w.backendType() != "" {
		b.WriteString("`-reconfigure` switches the working directory to the environment's state.")
	} else {
		fmt.Fprintf(&b, "Without a backend, each environment keeps its state in the workspace named after it, in `%s/<env>/`, which `infraharvest drift` reads.", tfrun.WorkspaceStateDir)
	}
	b.WriteString(" Plan each environment before its first apply: it should import every resource and change nothing else.\n\n")
	fmt.Fprintf(&b, "## After the first apply\n\nOnce every environment has been applied, delete `%s` and each `%s`, and leave `-var-file=%s` out of the commands.\n", rootfile.ImportsFileName, hclfile.ImportVarsPath("<env>"), hclfile.ImportVarsPath("<env>"))
	b.WriteString(w.arrivals())
	var secrets []string
	for _, v := range w.variables() {
		if v.sensitive {
			secrets = append(secrets, "`"+v.name+"`")
		}
	}
	for _, blk := range w.copied {
		if attr := blk.Body().GetAttribute("sensitive"); attr != nil && strings.TrimSpace(string(attr.Expr().BuildTokens(nil).Bytes())) == "true" {
			secrets = append(secrets, "`"+blk.Labels()[0]+"`")
		}
	}
	if len(secrets) > 0 {
		sort.Strings(secrets)
		fmt.Fprintf(&b, "\n## Secrets\n\nSet %s for each environment from your secret store, or with `TF_VAR_<name>`, never in a var file. infraharvest never writes secret values into the configuration.\n", strings.Join(secrets, ", "))
	}
	return []byte(b.String())
}

// title is the README's title: the stack's provider and region.
func (w *rootWriter) title() string {
	parts := []string{w.m.provider}
	if r := w.m.roots[0].region; r != "" {
		parts = append(parts, r)
	}
	return path.Join(parts...)
}

// arrivals lists what each environment's part of the merged root imports
// that other roots of the output hand over (see MergeRoot.Arrivals), at
// its address in the merged root, for the order to apply them in. It is
// empty if there is none.
func (w *rootWriter) arrivals() string {
	var rows strings.Builder
	for e, root := range w.m.roots {
		ids := w.m.imports(e)
		for _, a := range root.arrivals {
			at := a.To
			for _, address := range slices.Sorted(maps.Keys(ids)) {
				if ids[address] == a.ID && (strings.HasPrefix(address, a.Type+".") || strings.Contains(address, "."+a.Type+".")) {
					at = address
					break
				}
			}
			fmt.Fprintf(&rows, "| %s | `%s` | `%s` | `%s` |\n", w.m.envs[e], at, a.Root, a.From)
		}
	}
	if rows.Len() == 0 {
		return ""
	}
	return "\n## Moves\n\nThese resources come from other roots of the output, whose removed blocks make their state forget them. Apply the root each comes from first, then this root as the environment, which imports it: until then, both would manage it.\n\n| Environment | Imported | Comes from | Was |\n|---|---|---|---|\n" + rows.String()
}

// firstParseError returns the first error of parsing src, the content of
// the file name, without the source of the line it is on.
func firstParseError(name string, src []byte) string {
	_, diags := hclsyntax.ParseConfig(src, name, hcl.InitialPos)
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		msg := d.Summary
		if d.Detail != "" {
			msg += ": " + d.Detail
		}
		if d.Subject != nil {
			msg = fmt.Sprintf("line %d: %s", d.Subject.Start.Line, msg)
		}
		return msg
	}
	return "its layout fails"
}
