// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// The placeholders normalising puts in names and values for what differs
// between the environments of a stack by design (see normaliser).
const (
	envPlaceholder           = "{env}"
	accountPlaceholder       = "{account}"
	regionPlaceholder        = "{region}"
	resourceGroupPlaceholder = "{resource_group}"
)

// stackPlaceholders are the layout placeholders that tell the roots of
// one stack's environments apart, which its key leaves out.
var stackPlaceholders = []string{"{env}", "{account}", "{subscription}", "{project}"}

// stackKey returns the stack a root laid out by pattern belongs to: the
// pattern without its segments that hold a placeholder of
// stackPlaceholders, and with values (by placeholder, such as {provider})
// filling in the others, normalised by n (see normaliser.value) if set,
// as an Azure root's resource group may be named after its environment.
// Roots of different environments with the same key merge: the same
// provider, region and service.
func stackKey(pattern string, values map[string]string, n *normaliser) string {
	pairs := make([]string, 0, 2*len(values))
	for _, placeholder := range slices.Sorted(maps.Keys(values)) {
		pairs = append(pairs, placeholder, values[placeholder])
	}
	fill := strings.NewReplacer(pairs...)
	var kept []string
	for _, segment := range strings.Split(strings.ReplaceAll(pattern, "\\", "/"), "/") {
		if slices.ContainsFunc(stackPlaceholders, func(p string) bool { return strings.Contains(segment, p) }) {
			continue
		}
		segment = fill.Replace(segment)
		if n != nil {
			segment = n.value(segment)
		}
		kept = append(kept, segment)
	}
	return path.Clean(strings.ReplaceAll(strings.Join(kept, "/"), "\\", "/"))
}

// envRoot is one environment's finished root of a stack, as merging reads
// it.
type envRoot struct {
	env string
	// tokens are the words that stand for the environment inside names,
	// such as prd for prod (see config.Environment.Tokens); by default
	// env.
	tokens []string
	// scope is the root's account, subscription or project ID, region its
	// AWS region, and group its resource group, if any.
	scope, region, group string
	file                 *hclFile          // generated.tf
	importIDs            map[string]string // by resource address
	// data are the data sources the root reads, nil for none.
	data *hclFile

	// What the merged root's other files are written from (see
	// writeMergedRoot): the root's provider, its configuration without
	// credentials; its required_version; the tags its provider applies
	// to every resource (default_tags), if any; the file each resource
	// goes in, by address (see Import.File); its backend.tf and its
	// variables.tf, nil for none.
	provider        Provider
	requiredVersion string
	defaultTags     map[string]string
	files           map[string]string
	backend         []byte
	variables       *hclFile
}

// normaliser puts placeholders in one environment's labels, import IDs
// and string values: accountPlaceholder for its root's account,
// subscription or project, regionPlaceholder for its AWS region,
// resourceGroupPlaceholder for its resource group where the group's name
// holds none of the environment's tokens, and envPlaceholder for those
// tokens. Each is replaced where it is a whole word: between the ends of
// the string and characters other than letters and digits, such as - _ .
// / and :, so that shop-prd-db holds prd and
// arn:aws:iam::111111111111:role/ci the account, but production doesn't
// hold prod. The tokens are words of any case: shop-PRD-db and
// shop-Prd-db hold prd too.
type normaliser struct {
	words  []string // the tokens, longest first
	scope  string
	region string
	group  string // "" where the group's name holds a token
	// token is the word var.env stands for: the token the root's values
	// use most, as they write it (see chooseToken).
	token string
}

func newNormaliser(root envRoot) *normaliser {
	order := slices.Clone(root.tokens)
	if len(order) == 0 {
		order = []string{root.env}
	}
	words := slices.Clone(order)
	sort.SliceStable(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	n := &normaliser{words: words, scope: root.scope, region: root.region}
	if root.group != "" && !n.holdsToken(root.group) {
		n.group = root.group
	}
	n.token = chooseToken(order, root.file)
	return n
}

// chooseToken returns the token of order the string values of f's
// resources hold most often as a whole word, as they write it, in
// whatever case (PRD for prd, where the names say PRD); the first of
// order for a tie, or where none holds any.
func chooseToken(order []string, f *hclFile) string {
	counts := map[string]int{}
	var forms []string // as the values write the tokens, in order seen
	if f != nil {
		for _, r := range f.resources() {
			forms = countTokens(r.write.Body(), order, counts, forms)
		}
	}
	best := order[0]
	for _, word := range order {
		for _, form := range forms {
			if strings.EqualFold(form, word) && counts[form] > counts[best] {
				best = form
			}
		}
	}
	return best
}

// countTokens counts in counts each whole-word occurrence in body's string
// values of a token of order, as written, and returns forms with each
// way of writing one not seen before.
func countTokens(body *hclwrite.Body, order []string, counts map[string]int, forms []string) []string {
	for _, attr := range body.Attributes() {
		for _, t := range attr.Expr().BuildTokens(nil) {
			if t.Type != hclsyntax.TokenQuotedLit && t.Type != hclsyntax.TokenStringLit {
				continue
			}
			for _, word := range order {
				for _, form := range wordForms(string(t.Bytes), word) {
					if counts[form] == 0 {
						forms = append(forms, form)
					}
					counts[form]++
				}
			}
		}
	}
	for _, b := range body.Blocks() {
		forms = countTokens(b.Body(), order, counts, forms)
	}
	return forms
}

// holdsToken reports whether s holds one of n's tokens as a whole word.
func (n *normaliser) holdsToken(s string) bool {
	return slices.ContainsFunc(n.words, func(w string) bool { return indexWord(s, w, true) >= 0 })
}

// value returns s with n's placeholders in it.
func (n *normaliser) value(s string) string {
	if n.scope != "" {
		s = replaceWord(s, n.scope, accountPlaceholder, false)
	}
	if n.region != "" {
		s = replaceWord(s, n.region, regionPlaceholder, false)
	}
	if n.group != "" {
		s = replaceWord(s, n.group, resourceGroupPlaceholder, false)
	}
	return n.envValue(s)
}

// envValue returns s with envPlaceholder for n's tokens, in any case, and
// no other placeholder.
func (n *normaliser) envValue(s string) string {
	for _, w := range n.words {
		s = replaceWord(s, w, envPlaceholder, true)
	}
	return s
}

// denormalise returns s, a value with placeholders, as n's environment
// has it, with its chosen token for envPlaceholder.
func (n *normaliser) denormalise(s string) string {
	return strings.NewReplacer(envPlaceholder, n.token, accountPlaceholder, n.scope, regionPlaceholder, n.region, resourceGroupPlaceholder, n.group).Replace(s)
}

// label returns a block label without the words of n's tokens, scope, region
// and group, which merged labels leave out: shop_prd_web (or web, once
// stripPrefix took shop_prd_) and logs_prd become web and logs. A label
// with nothing else becomes main.
func (n *normaliser) label(label string) string {
	// Without the r_ that Label puts before a digit, which may be the
	// scope's.
	if rest, ok := strings.CutPrefix(label, "r_"); ok && startsWithDigit(rest) {
		label = rest
	}
	s := "_" + label + "_"
	drop := make([]string, 0, len(n.words)+2)
	for _, w := range append(slices.Clone(n.words), n.scope, n.region, n.group) {
		if words := labelWords(w); len(words) > 0 {
			drop = append(drop, strings.Join(words, "_"))
		}
	}
	for _, w := range drop {
		for strings.Contains(s, "_"+w+"_") {
			s = strings.ReplaceAll(s, "_"+w+"_", "_")
		}
	}
	s = strings.Trim(s, "_")
	switch {
	case s == "":
		return mainLabel
	case startsWithDigit(s):
		return "r_" + s
	}
	return s
}

// mapLiterals returns tokens with fn applied to their string literals,
// leaving tokens as they are.
func mapLiterals(tokens hclwrite.Tokens, fn func(string) string) hclwrite.Tokens {
	out := make(hclwrite.Tokens, len(tokens))
	for i, t := range tokens {
		c := *t
		if t.Type == hclsyntax.TokenQuotedLit || t.Type == hclsyntax.TokenStringLit {
			c.Bytes = []byte(fn(string(t.Bytes)))
		}
		out[i] = &c
	}
	return out
}

// wordForms returns each whole-word occurrence of word in s, in any case
// (see indexWord), as s writes it.
func wordForms(s, word string) []string {
	var forms []string
	for {
		i := indexWord(s, word, true)
		if i < 0 {
			return forms
		}
		forms = append(forms, s[i:i+len(word)])
		s = s[i+len(word):]
	}
}

// replaceWord returns s with each whole-word occurrence of word (see
// indexWord, and fold) replaced by placeholder.
func replaceWord(s, word, placeholder string, fold bool) string {
	var b strings.Builder
	for {
		i := indexWord(s, word, fold)
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		b.WriteString(placeholder)
		s = s[i+len(word):]
	}
	b.WriteString(s)
	return b.String()
}

// indexWord returns the index of the first occurrence of word in s that
// is a whole word: with no letter or digit next to it. One after $ or %,
// which would make a template sequence of the placeholder, or inside a
// placeholder, doesn't count. With fold, the case of ASCII letters
// doesn't count either: PRD and Prd are prd. It returns -1 for none.
func indexWord(s, word string, fold bool) int {
	if word == "" {
		return -1
	}
	// in is s as word is looked for in it: the same bytes but for case.
	in := s
	if fold {
		in, word = lowerASCII(s), lowerASCII(word)
	}
	for from := 0; from <= len(s); {
		i := strings.Index(in[from:], word)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(word)
		before := i == 0 || (!isWordByte(s[i-1]) && s[i-1] != '$' && s[i-1] != '%')
		after := end == len(s) || !isWordByte(s[end])
		inPlaceholder := i > 0 && s[i-1] == '{' && end < len(s) && s[end] == '}'
		if before && after && !inPlaceholder {
			return i
		}
		from = i + 1
	}
	return -1
}

// lowerASCII returns s with its ASCII letters in lower case, and every
// other byte as it is, so that indexes in it are indexes in s.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// isWordByte reports whether b is part of a word: a letter, a digit, or a
// byte of a character beyond ASCII.
func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b >= 0x80
}

// matchRule is how a resource was matched across environments.
type matchRule int

const (
	notMatched   matchRule = iota // a resource of one environment
	byImportID                    // the same type and normalised import ID
	byLabel                       // the same type and normalised label
	byReferences                  // the same type, referring to the same matched resources
)

// envResource is a resource of one environment's root.
type envResource struct {
	env          int // the index of its root
	block        resourceBlock
	resourceType string
	id           string // its import ID, "" if unknown
	normalID     string // id normalised, "" if unknown
	normalLabel  string
	refs         []*envResource // the resources of its root it refers to
	stack        *stackResource // what it is part of, once matched
	// isolated keeps it apart from the other environments' resources (see
	// mergeControls.isolated).
	isolated bool
	// moduleIDs are, for a module call, the import IDs of the resources in
	// it, by their addresses in it, such as aws_s3_bucket.this[0].
	moduleIDs map[string]string
}

// stackResource is a resource of a stack's merged root: the resources it
// stands for in each environment that has one.
type stackResource struct {
	resourceType string
	label        string
	rule         matchRule
	members      []*envResource // by environment, as the roots are; nil where none
	seq          int            // the order it was matched in

	// residue says why the environments' resources it was matched from
	// weren't merged: it is one environment's part of them, "" if merged.
	residue string
	// forEach is the resource it is a key of, by its label, or nil.
	forEach *forEachGroup
	// deploy is the deploy_<feature> variable that counts it, and the
	// other resources of its feature, for a resource some environments
	// have and no forEach holds (see stackMerge.features).
	deploy *envVariable
	// body is its arguments across the environments that have it (see
	// mergeBody); nil in a forEach.
	body *mergedBody
}

// coverage is the number of environments that have r.
func (r *stackResource) coverage() int {
	n := 0
	for _, m := range r.members {
		if m != nil {
			n++
		}
	}
	return n
}

// address is r's resource block's address in the merged root: its
// forEach's, for a key of one.
func (r *stackResource) address() string {
	if r.forEach != nil {
		return r.forEach.address()
	}
	return r.resourceType + "." + r.label
}

// instance returns the address of r's instance in the merged root, as
// references and import blocks write it: type.label, type.label[0] for
// one deploy counts, and type.label["key"] in a forEach.
func (r *stackResource) instance() hcl.Traversal {
	switch {
	case r.forEach != nil:
		return hcl.Traversal{hcl.TraverseRoot{Name: r.resourceType}, hcl.TraverseAttr{Name: r.forEach.label}, hcl.TraverseIndex{Key: cty.StringVal(r.label)}}
	case r.deploy != nil:
		return hcl.Traversal{hcl.TraverseRoot{Name: r.resourceType}, hcl.TraverseAttr{Name: r.label}, hcl.TraverseIndex{Key: cty.NumberIntVal(0)}}
	}
	return hcl.Traversal{hcl.TraverseRoot{Name: r.resourceType}, hcl.TraverseAttr{Name: r.label}}
}

// matchResources matches the resources of roots, one per environment of
// a stack, whose normalisers are norms, in order: by type and normalised
// import ID; by type and normalised label, where the label is unique in
// its root; and by type and the matched resources they refer to, where
// one resource of each root is left. Each match takes each root's only
// candidate, so two resources of one root never match one. A resource
// that matches none is a stack resource of its environment alone. It
// returns the stack's resources sorted by address, with labels unique per
// type (see assignLabels).
func matchResources(roots []envRoot, norms []*normaliser) []*stackResource {
	return matchResourcesWith(roots, norms, nil)
}

// matchResourcesWith is matchResources, with the resources isolated names,
// by mergeKey, matched with no other environment's (see
// mergeControls.isolated).
func matchResourcesWith(roots []envRoot, norms []*normaliser, isolated map[string]bool) []*stackResource {
	byEnv := make([][]*envResource, len(roots))
	labels := make([]map[string]int, len(roots))
	for e, root := range roots {
		labels[e] = map[string]int{}
		index := map[string]*envResource{}
		for _, b := range stackBlocks(root.file) {
			typ, label, _ := strings.Cut(b.address, ".")
			r := &envResource{env: e, block: b, resourceType: typ, normalLabel: norms[e].label(label)}
			r.isolated = isolated[mergeKey(e, b.address)]
			if typ == moduleType {
				r.moduleIDs = moduleImportIDs(root.importIDs, label)
			}
			if id := root.importIDs[b.address]; id != "" {
				r.id, r.normalID = id, norms[e].value(id)
			}
			if !r.isolated {
				labels[e][typ+"."+r.normalLabel]++
			}
			index[b.address] = r
			byEnv[e] = append(byEnv[e], r)
		}
		for _, r := range byEnv[e] {
			for _, a := range stackReferences(r.block.syntax.Body) {
				if t, ok := index[a]; ok && t != r {
					r.refs = append(r.refs, t)
				}
			}
		}
	}
	var stack []*stackResource
	add := func(rule matchRule, members []*envResource) {
		s := &stackResource{resourceType: members[0].resourceType, rule: rule, members: make([]*envResource, len(roots)), seq: len(stack)}
		for _, m := range members {
			s.members[m.env] = m
			m.stack = s
		}
		stack = append(stack, s)
	}
	matchBy(byEnv, func(r *envResource) string {
		if r.normalID == "" {
			return ""
		}
		return r.resourceType + "\x00" + r.normalID
	}, func(members []*envResource) { add(byImportID, members) })
	matchBy(byEnv, func(r *envResource) string {
		if labels[r.env][r.resourceType+"."+r.normalLabel] != 1 {
			return ""
		}
		return r.resourceType + "\x00" + r.normalLabel
	}, func(members []*envResource) { add(byLabel, members) })
	// A match by references may let others refer to matched resources
	// only.
	for matched := true; matched; {
		matched = matchBy(byEnv, referenceKey, func(members []*envResource) { add(byReferences, members) })
	}
	for _, rs := range byEnv {
		for _, r := range rs {
			if r.stack == nil {
				add(notMatched, []*envResource{r})
			}
		}
	}
	envs := make([]string, len(roots))
	for i, root := range roots {
		envs[i] = root.env
	}
	assignLabels(stack, envs)
	sort.Slice(stack, func(i, j int) bool { return stack[i].address() < stack[j].address() })
	return stack
}

// referenceKey is the key of a match by references: the resource's type
// and the matched resources it refers to, where it refers to some and to
// no other.
func referenceKey(r *envResource) string {
	if len(r.refs) == 0 {
		return ""
	}
	seqs := make([]int, 0, len(r.refs))
	for _, t := range r.refs {
		if t.stack == nil {
			return ""
		}
		seqs = append(seqs, t.stack.seq)
	}
	slices.Sort(seqs)
	seqs = slices.Compact(seqs)
	parts := make([]string, len(seqs))
	for i, s := range seqs {
		parts[i] = strconv.Itoa(s)
	}
	return r.resourceType + "\x00" + strings.Join(parts, ",")
}

// matchBy matches the resources of byEnv not yet matched whose key, by
// key ("" for none), is the same, where each environment has at most one
// with it and at least two have one; match gets each match's resources.
// It reports whether it matched any.
func matchBy(byEnv [][]*envResource, key func(*envResource) string, match func([]*envResource)) bool {
	candidates := map[string][][]*envResource{}
	for e, rs := range byEnv {
		for _, r := range rs {
			if r.stack != nil || r.isolated {
				continue
			}
			k := key(r)
			if k == "" {
				continue
			}
			if candidates[k] == nil {
				candidates[k] = make([][]*envResource, len(byEnv))
			}
			candidates[k][e] = append(candidates[k][e], r)
		}
	}
	matched := false
	for _, k := range slices.Sorted(maps.Keys(candidates)) {
		var members []*envResource
		unique := true
		for _, rs := range candidates[k] {
			switch len(rs) {
			case 0:
			case 1:
				members = append(members, rs[0])
			default:
				unique = false
			}
		}
		if unique && len(members) >= 2 {
			match(members)
			matched = true
		}
	}
	return matched
}

// assignLabels labels each of resources, the resources of one stack, by
// the normalised label of its first environment's resource, unique per
// type: where two have the same, the one more environments have keeps
// it, and the other gets its environment's name after it, for a resource
// of one environment, or _2, _3, ...
func assignLabels(resources []*stackResource, envs []string) {
	type candidate struct {
		r     *stackResource
		base  string
		first int
	}
	all := make([]candidate, 0, len(resources))
	for _, r := range resources {
		for e, m := range r.members {
			if m != nil {
				all = append(all, candidate{r, m.normalLabel, e})
				break
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.r.resourceType != b.r.resourceType {
			return a.r.resourceType < b.r.resourceType
		}
		if a.base != b.base {
			return a.base < b.base
		}
		// What is kept apart comes last: what was merged keeps its label.
		if ai, bi := a.r.members[a.first].isolated, b.r.members[b.first].isolated; ai != bi {
			return !ai
		}
		if a.r.coverage() != b.r.coverage() {
			return a.r.coverage() > b.r.coverage()
		}
		if a.first != b.first {
			return a.first < b.first
		}
		return a.r.members[a.first].block.address < b.r.members[b.first].block.address
	})
	used := map[string]bool{}
	for _, l := range all {
		label := l.base
		if used[l.r.resourceType+"."+label] && l.r.coverage() == 1 {
			label = l.base + "_" + Label(envs[l.first])
		}
		label = uniqueIn(used, l.r.resourceType, label)
		l.r.label = label
	}
}

// uniqueIn returns label, or label with _2, _3, ... after it, whichever
// first isn't a label of resourceType in used, and records it there.
func uniqueIn(used map[string]bool, resourceType, label string) string {
	unique := label
	for n := 2; used[resourceType+"."+unique]; n++ {
		unique = fmt.Sprintf("%s_%d", label, n)
	}
	used[resourceType+"."+unique] = true
	return unique
}
