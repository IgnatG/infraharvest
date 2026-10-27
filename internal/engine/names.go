// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var notInLabel = regexp.MustCompile(`[^a-z0-9]+`)

// wordBoundaries are the word boundaries inside a camelCase or PascalCase
// name: the end of an acronym before a capitalised word (HTTPApi), a
// lower-case letter before an upper-case one (allowHttp), and a digit
// before a capitalised word (Ipv4Address).
var wordBoundaries = []*regexp.Regexp{
	regexp.MustCompile(`([A-Z])([A-Z][a-z])`),
	regexp.MustCompile(`([a-z])([A-Z])`),
	regexp.MustCompile(`([0-9])([A-Z][a-z])`),
}

// Label turns a resource's name into a resource block label: snake_case,
// with the words of a camelCase name kept apart (AllowHttpApi becomes
// allow_http_api), and a run of words that repeats the run before it
// written once (shop_shop_web becomes shop_web). It puts "r_" in front if
// the label would start with a digit, and gives "resource" if nothing is
// left.
func Label(name string) string {
	label := strings.Join(labelWords(name), "_")
	switch {
	case label == "":
		return "resource"
	case startsWithDigit(label):
		return "r_" + label
	}
	return label
}

// labelWords returns the lower-case words of name that Label joins.
func labelWords(name string) []string {
	for _, boundary := range wordBoundaries {
		name = boundary.ReplaceAllString(name, "${1}_${2}")
	}
	words := strings.FieldsFunc(notInLabel.ReplaceAllString(strings.ToLower(name), "_"), func(r rune) bool { return r == '_' })
	return withoutRepeats(words)
}

// withoutRepeats drops each run of words that repeats the run right before
// it, longest runs first, so that a name made of a parent's name and a
// child's that starts with it (cluster shop, service shop-web) says it
// once. Runs of numbers only are kept, as in an IP address (10_0_0_1).
func withoutRepeats(words []string) []string {
	for n := len(words) / 2; n >= 1; n-- {
		for i := 0; i+2*n <= len(words); {
			run := words[i : i+n]
			if slices.Equal(run, words[i+n:i+2*n]) && !slices.ContainsFunc(run, isNumber) {
				words = slices.Delete(words, i+n, i+2*n)
				continue
			}
			i++
		}
	}
	return words
}

// isNumber reports whether word is digits only.
func isNumber(word string) bool {
	return word != "" && strings.Trim(word, "0123456789") == ""
}

// startsWithDigit reports whether s starts with a digit, which a label
// can't.
func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

// Names are names a root already uses, which Generate gives nothing it
// adds: resources as type.name, and data.type.name, module.name, var.name
// and local.name.
type Names map[string]bool

// hasType reports whether n has a resource of resourceType.
func (n Names) hasType(resourceType string) bool {
	for name := range n {
		if typ, _, ok := strings.Cut(name, "."); ok && typ == resourceType {
			return true
		}
	}
	return false
}

// relabelled returns imports with Name turned into a label (see Label),
// tidied if tidy is set (see tidyLabels), and made unique per resource
// type with _2, _3, ... in import ID order, so the labels don't depend on
// the order resources were listed in. Labels in taken are left to the
// resources that have them. For a root generated again, previous, if set,
// says what an earlier import generated: a resource keeps the label it
// had where its name still gives it (see keepsLabel), so that adding or
// removing others doesn't move it, and no other resource takes an address
// previous reserves (see Previous.reserved), which another resource had
// or a moved block names.
func relabelled(imports []Import, taken Names, tidy bool, previous *Previous) []Import {
	sorted := make([]Import, len(imports))
	bases := map[External]string{}
	for i, imp := range imports {
		imp.Name = Label(imp.Name)
		if tidy {
			bases[ResourceKey(imp.Type, imp.ID)] = withoutTypeWords(imp.Type, imp.Name)
		} else {
			bases[ResourceKey(imp.Type, imp.ID)] = imp.Name
		}
		sorted[i] = imp
	}
	if tidy {
		tidyLabels(sorted, taken)
	}
	reserved := previous.reserved()
	free := func(imp Import, name string) bool {
		owner, ok := reserved[imp.Type+"."+name]
		return !ok || owner == ResourceKey(imp.Type, imp.ID)
	}
	labels := previous.labels()
	kept := map[External]bool{}
	for i, imp := range sorted {
		key := ResourceKey(imp.Type, imp.ID)
		if old, ok := labels[key]; ok && keepsLabel(old, bases[key], tidy) && !taken[imp.Type+"."+old] && free(imp, old) {
			sorted[i].Name = old
			kept[key] = true
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	own := map[string]bool{}
	for _, imp := range sorted {
		own[imp.Type+"."+imp.Name] = true
	}
	used := maps.Clone(taken)
	if used == nil {
		used = Names{}
	}
	// The labels kept first, so that no other resource takes them.
	for _, imp := range sorted {
		key := ResourceKey(imp.Type, imp.ID)
		if kept[key] && used[imp.Type+"."+imp.Name] {
			kept[key] = false
		} else if kept[key] {
			used[imp.Type+"."+imp.Name] = true
		}
	}
	for i, imp := range sorted {
		if kept[ResourceKey(imp.Type, imp.ID)] {
			continue
		}
		name := imp.Name
		for n := 2; used[imp.Type+"."+name] || !free(imp, name); n++ {
			// Skip labels another resource has as its own.
			if candidate := fmt.Sprintf("%s_%d", imp.Name, n); !own[imp.Type+"."+candidate] {
				name = candidate
			}
		}
		used[imp.Type+"."+name] = true
		sorted[i].Name = name
	}
	return sorted
}

// keepsLabel reports whether old, the label an earlier import gave a
// resource, still fits it now that its name gives base (see relabelled):
// old is base, or base without words a prefix took (see stripPrefix),
// either with _2, _3, ... after it, or, with tidy, main, which says only
// that the root had no other resource of the type. A label that doesn't
// fit says the resource's name has changed, such as in the cloud.
func keepsLabel(old, base string, tidy bool) bool {
	if tidy && old == mainLabel {
		return true
	}
	fits := func(label string) bool {
		return label == base || strings.HasSuffix(base, "_"+label)
	}
	if fits(old) {
		return true
	}
	i := strings.LastIndexByte(old, '_')
	return i > 0 && isNumber(old[i+1:]) && fits(old[:i])
}

// mainLabel labels a resource whose type a root has no other of, as the
// HashiCorp style guide suggests.
const mainLabel = "main"

// tidyLabels rewrites in place the labels of imports, the resources
// Generate adds to one root, the way a person names them:
//
//   - without the words of the resource type at either end:
//     aws_sqs_queue.orders rather than aws_sqs_queue.orders_queue (see
//     withoutTypeWords);
//   - without a prefix most of the labels share, such as a project's name,
//     where that leaves a label unique within its type (see stripPrefix);
//   - main for a resource whose type the root has no other of, among
//     imports or in taken.
//
// The labels depend on the imports and taken alone, not on their order.
func tidyLabels(imports []Import, taken Names) {
	for i := range imports {
		imports[i].Name = withoutTypeWords(imports[i].Type, imports[i].Name)
	}
	stripPrefix(imports, taken)
	perType := map[string]int{}
	for _, imp := range imports {
		perType[imp.Type]++
	}
	for i, imp := range imports {
		if perType[imp.Type] == 1 && !taken.hasType(imp.Type) {
			imports[i].Name = mainLabel
		}
	}
}

// withoutTypeWords returns label without the words of resourceType, past
// its provider's, that it starts or ends with: the longest run of the
// type's last words at either end, such as queue or sqs_queue for
// aws_sqs_queue, and task_definition for aws_ecs_task_definition. A label
// that is nothing else, or would then start with a digit, is kept.
func withoutTypeWords(resourceType, label string) string {
	_, rest, ok := strings.Cut(resourceType, "_")
	if !ok {
		return label
	}
	typeWords := strings.Split(rest, "_")
	words := strings.Split(label, "_")
	for k := len(typeWords); k >= 1; k-- {
		if tail := typeWords[len(typeWords)-k:]; len(words) > k && slices.Equal(words[:k], tail) && !startsWithDigit(words[k]) {
			words = words[k:]
			break
		}
	}
	for k := len(typeWords); k >= 1; k-- {
		if tail := typeWords[len(typeWords)-k:]; len(words) > k && slices.Equal(words[len(words)-k:], tail) {
			words = words[:len(words)-k]
			break
		}
	}
	return strings.Join(words, "_")
}

// stripPrefix drops from the labels of imports the prefix of words most of
// them share (see sharedPrefix). A label keeps it where dropping it would
// leave a label starting with a digit, or one that another resource of the
// same type has, in imports or in taken.
func stripPrefix(imports []Import, taken Names) {
	prefix := sharedPrefix(imports)
	if prefix == "" {
		return
	}
	stripped := make([]string, len(imports))
	for i, imp := range imports {
		if rest, ok := strings.CutPrefix(imp.Name, prefix); ok && rest != "" && !startsWithDigit(rest) {
			stripped[i] = rest
		}
	}
	final := map[string]int{}
	for i, imp := range imports {
		final[imp.Type+"."+cmp.Or(stripped[i], imp.Name)]++
	}
	for i, imp := range imports {
		if key := imp.Type + "." + stripped[i]; stripped[i] != "" && final[key] == 1 && !taken[key] {
			imports[i].Name = stripped[i]
		}
	}
}

// sharedPrefix returns the longest run of leading words, with the
// underscore after it, that more than half of the labels of imports, and
// at least two, start with and go on past: "shop_prod_" for shop_prod_web,
// shop_prod_api and logs. It returns "" if there is none.
func sharedPrefix(imports []Import) string {
	counts := map[string]int{}
	for _, imp := range imports {
		words := strings.Split(imp.Name, "_")
		for k := 1; k < len(words); k++ {
			counts[strings.Join(words[:k], "_")+"_"]++
		}
	}
	best := ""
	for prefix, n := range counts {
		if n < 2 || 2*n <= len(imports) {
			continue
		}
		if len(prefix) > len(best) || (len(prefix) == len(best) && prefix < best) {
			best = prefix
		}
	}
	return best
}

// callName is the name of a module call built around the resource at
// address: the resource's label, or, for main, which says only that the
// root has one resource of the type, the type without its provider, as in
// module "vpc" for aws_vpc.main.
func callName(address string) string {
	resourceType, label, _ := strings.Cut(address, ".")
	if _, rest, ok := strings.Cut(resourceType, "_"); ok && label == mainLabel {
		return rest
	}
	return label
}
