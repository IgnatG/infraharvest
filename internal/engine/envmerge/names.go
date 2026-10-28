// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// firstWord is the first word of r's name in the merged root (see
// hclfile.CallName).
func firstWord(r *stackResource) string {
	word, _, _ := strings.Cut(hclfile.CallName(r.resourceType+"."+r.label), "_")
	return word
}

// commonWord returns the first word (see firstWord) most of resources
// have; the first in order of a tie.
func commonWord(resources []*stackResource) string {
	counts := map[string]int{}
	for _, r := range resources {
		counts[firstWord(r)]++
	}
	best := ""
	for _, w := range slices.Sorted(maps.Keys(counts)) {
		if best == "" || counts[w] > counts[best] {
			best = w
		}
	}
	return best
}

// nameVariables collects the variables of the merged root, makes their
// names unique, and sets their defaults.
func (m *stackMerge) nameVariables() {
	env := &envVariable{name: envVariableName, what: "token that stands for the environment in names", ty: cty.String, values: map[int]hclwrite.Tokens{}}
	for e, n := range m.norms {
		env.values[e] = hclwrite.TokensForValue(cty.StringVal(n.token))
	}
	variables := []*envVariable{env}
	collect := func(b *mergedBody) {
		b.visit(func(a *mergedAttribute) {
			if a.variable != nil {
				variables = append(variables, a.variable)
			}
		}, func(blk *mergedBlock) {
			if blk.variable != nil {
				variables = append(variables, blk.variable)
			}
		})
	}
	deploys := map[*envVariable]bool{}
	for _, r := range m.resources {
		if r.deploy != nil && !deploys[r.deploy] {
			deploys[r.deploy] = true
			variables = append(variables, r.deploy)
		}
		if r.body != nil {
			collect(r.body)
		}
	}
	for _, g := range m.forEach {
		collect(g.body)
		if g.variable != nil {
			variables = append(variables, g.variable)
		}
	}
	for _, d := range m.rootData {
		collect(d.body)
	}
	used := map[string]bool{hclfile.ImportIDsVariableName: true, hclfile.ImportIdentitiesVariableName: true, resourceGroupVariableName: true}
	for _, v := range variables {
		v.name = uniqueEnvName(used, v.name, v.resourceType)
		if !v.hasDefault {
			v.setDefault(len(m.envs))
		}
	}
	m.variables = variables
}

// uniqueEnvName returns name, or where used has it, name after
// resourceType's name without its provider, or with _2, _3, ... after
// it, whichever first isn't in used; and records it there.
func uniqueEnvName(used map[string]bool, name, resourceType string) string {
	if _, rest, ok := strings.Cut(resourceType, "_"); ok && used[name] {
		name = rest + "_" + name
	}
	unique := name
	for n := 2; used[unique]; n++ {
		unique = fmt.Sprintf("%s_%d", name, n)
	}
	used[unique] = true
	return unique
}

// residueName returns the argument that is r's name, where r is residue
// and its name a literal string, and the name with envPlaceholder for its
// environment's tokens (see normaliser.envValue): a template its
// environment's var files give its value back from, as
// local.naming_prefix does. The template is nil where the name holds no
// token, or doesn't come back as it is, and the name stays as it is.
func (m *stackMerge) residueName(r *stackResource) (*mergedAttribute, hclwrite.Tokens) {
	if r.residue == "" || r.body == nil {
		return nil, nil
	}
	e := slices.IndexFunc(r.members, func(member *envResource) bool { return member != nil })
	if e < 0 {
		return nil, nil
	}
	n := m.norms[e]
	for _, a := range r.body.attributes {
		if a.kind != sameValue || !isNameAttribute(r.resourceType, a.name) {
			continue
		}
		if v, ok := literalValue(a.value); !ok || v.Type() != cty.String {
			continue
		}
		normal := mapLiterals(a.value, n.envValue)
		if !holds([]hclwrite.Tokens{normal}, envPlaceholder) || exprKey(mapLiterals(normal, n.denormalise)) != exprKey(a.value) {
			return a, nil
		}
		return a, normal
	}
	return nil, nil
}

// holds reports whether the string literals of any of templates hold
// placeholder.
func holds(templates []hclwrite.Tokens, placeholder string) bool {
	for _, tokens := range templates {
		for _, t := range tokens {
			if (t.Type == hclsyntax.TokenQuotedLit || t.Type == hclsyntax.TokenStringLit) && strings.Contains(string(t.Bytes), placeholder) {
				return true
			}
		}
	}
	return false
}

// isNameAttribute reports whether attr of resourceType is the resource's
// name: name, bucket or identifier, or <word>_name for a word of the
// type, as function_name of aws_lambda_function.
func isNameAttribute(resourceType, attr string) bool {
	switch attr {
	case "name", "bucket", "identifier":
		return true
	}
	word, ok := strings.CutSuffix(attr, "_name")
	_, rest, _ := strings.Cut(resourceType, "_")
	return ok && slices.Contains(strings.Split(rest, "_"), word)
}

// withNamingPrefix returns s with each occurrence of prefix, the naming
// prefix with placeholders, that no letter or digit comes right before
// made local.naming_prefix.
func withNamingPrefix(s, prefix string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		for i > 0 && isWordByte(s[i-1]) {
			next := strings.Index(s[i+1:], prefix)
			if next < 0 {
				i = -1
				break
			}
			i += 1 + next
		}
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		b.WriteString("${local.naming_prefix}")
		s = s[i+len(prefix):]
	}
	b.WriteString(s)
	return b.String()
}

// workloadOf returns what the names in templates, values with
// placeholders, most often have before -{env}: shop for shop-{env}-db and
// arn:aws:s3:::shop-{env}-logs. A tie goes to the shorter, then the first
// in order; "" for none.
func workloadOf(templates []hclwrite.Tokens) string {
	counts := map[string]int{}
	for _, tokens := range templates {
		for _, t := range tokens {
			if t.Type != hclsyntax.TokenQuotedLit && t.Type != hclsyntax.TokenStringLit {
				continue
			}
			s := string(t.Bytes)
			for from := 0; ; {
				i := strings.Index(s[from:], "-"+envPlaceholder)
				if i < 0 {
					break
				}
				i += from
				start := i
				for start > 0 && (isWordByte(s[start-1]) || s[start-1] == '-' || s[start-1] == '_') {
					start--
				}
				if w := strings.TrimLeft(s[start:i], "-_"); w != "" && !hclfile.StartsWithDigit(w) {
					counts[w]++
				}
				from = i + 1
			}
		}
	}
	best := ""
	for _, w := range slices.Sorted(maps.Keys(counts)) {
		if n := counts[w]; best == "" || n > counts[best] || (n == counts[best] && len(w) < len(best)) {
			best = w
		}
	}
	return best
}

// pluralName is the name of a forEach's variable: its resource type
// without the provider, in the plural, as sqs_queues for aws_sqs_queue.
func pluralName(resourceType string) string {
	_, name, ok := strings.Cut(resourceType, "_")
	if !ok {
		name = resourceType
	}
	switch {
	case strings.HasSuffix(name, "s"), strings.HasSuffix(name, "x"), strings.HasSuffix(name, "sh"), strings.HasSuffix(name, "ch"):
		return name + "es"
	case strings.HasSuffix(name, "y") && len(name) > 1 && !strings.ContainsRune("aeiou", rune(name[len(name)-2])):
		return name[:len(name)-1] + "ies"
	}
	return name + "s"
}
