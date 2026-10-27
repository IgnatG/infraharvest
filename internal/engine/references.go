// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// referenceTarget is a resource attribute a literal can refer to instead.
type referenceTarget struct {
	address string
	// attribute is the attribute's name, or its path in a nested block,
	// such as identity[0].principal_id (see nestedIdentities).
	attribute string
	// data is set for a data source (see addDataSourcesSkipping).
	data bool
}

func (t referenceTarget) traversal() hcl.Traversal {
	typ, name, _ := strings.Cut(t.address, ".")
	steps := append(hcl.Traversal{hcl.TraverseAttr{Name: typ}, hcl.TraverseAttr{Name: name}}, attributeSteps(t.attribute)...)
	if t.data {
		return append(hcl.Traversal{hcl.TraverseRoot{Name: "data"}}, steps...)
	}
	return append(hcl.Traversal{hcl.TraverseRoot{Name: typ}}, steps[1:]...)
}

// attributeSteps are the steps of an attribute's path: its name, or the
// block, index and name of one in a nested block, as identity[0].principal_id.
func attributeSteps(attribute string) hcl.Traversal {
	var steps hcl.Traversal
	for _, step := range strings.Split(attribute, ".") {
		name, index, indexed := strings.Cut(step, "[")
		steps = append(steps, hcl.TraverseAttr{Name: name})
		if n, err := strconv.Atoi(strings.TrimSuffix(index, "]")); indexed && err == nil {
			steps = append(steps, hcl.TraverseIndex{Key: cty.NumberIntVal(int64(n))})
		}
	}
	return steps
}

// node is the target's name in the dependency graph.
func (t referenceTarget) node() string {
	if t.data {
		return "data." + t.address
	}
	return t.address
}

// addReferencesSkipping replaces literals in generated.tf that are another
// resource's id, ARN or self link, by its imported values, with a
// reference to that attribute: vpc_id = aws_vpc.main.id. A value counts
// if it identifies a resource on its own (see referenceable), or if the
// argument is named after the resource's type, like an S3 bucket name in
// bucket = "...". An argument that names a parent resource refers to it
// too (see parentNames): resource_group_name =
// azurerm_resource_group.main.name. Beyond those, it finds what
// identities does: short IDs, names and the other attributes that
// identify a resource, and IDs inside longer strings.
// It doesn't make a resource refer to itself or two resources refer to
// each other, directly or not. A resource skip has at level 1 gets only
// the references of the index and its parents, which identify the
// resource by its whole ID, ARN or name; one at level 2 or more gets none
// (see verifyEach). It returns the resources it changed.
func addReferencesSkipping(dir string, values map[string]map[string]any, skip map[string]int) (map[string]bool, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	r := &resolver{
		index:   referenceIndex(values),
		parents: parentIndex(values),
		ids:     identityIndex(values, writtenArguments(generated)),
		g:       dependencies{},
		src:     generated.src,
	}
	edited := map[string]bool{}
	for _, res := range generated.resources() {
		level := skip[res.address]
		if level >= 2 {
			continue
		}
		if r.attributes(res.address, res.syntax.Body, res.write.Body(), "", level == 0) {
			edited[res.address] = true
		}
	}
	if len(edited) == 0 {
		return nil, nil
	}
	return edited, generated.save()
}

// googleAPIs and computeAPIs start the self links of Google Compute Engine
// resources, as the provider writes them and as some APIs return them.
const (
	googleAPIs  = "https://www.googleapis.com/compute/"
	computeAPIs = "https://compute.googleapis.com/compute/"
)

// referenceIndex maps each id, ARN, self link, email and IAM member value,
// such as a service account's serviceAccount:<email>, to the attributes
// to refer to instead, one per resource that has the value, by address.
// Which of them a literal refers to, if any, depends on where it is (see
// uniqueTarget).
func referenceIndex(values map[string]map[string]any) map[string][]referenceTarget {
	index := map[string][]referenceTarget{}
	for address, attrs := range values {
		for _, key := range []string{"id", "arn", "self_link", "email", "member"} {
			v, ok := attrs[key].(string)
			if !ok || v == "" || slices.ContainsFunc(index[v], func(t referenceTarget) bool { return t.address == address }) {
				continue
			}
			// Only an identity, which has an email, is the member it names; an
			// IAM grant names its member too.
			if email, _ := attrs["email"].(string); key == "member" && email == "" {
				continue
			}
			attribute := "id"
			switch {
			case attrs["arn"] == v:
				attribute = "arn"
			case attrs["self_link"] == v:
				attribute = "self_link"
			case attrs["name"] == v:
				attribute = "name"
			case attrs["bucket"] == v:
				attribute = "bucket"
			case attrs["email"] == v:
				attribute = "email"
			case attrs["member"] == v:
				attribute = "member"
			}
			index[v] = append(index[v], referenceTarget{address: address, attribute: attribute})
			// Some Google APIs name a resource by its self link on the
			// service's own host, as a DNS zone's network_url does:
			// https://compute.googleapis.com/compute/v1/... for
			// https://www.googleapis.com/compute/v1/...
			if rest, ok := strings.CutPrefix(v, googleAPIs); ok && key == "self_link" {
				alias := computeAPIs + rest
				index[alias] = append(index[alias], referenceTarget{address: address, attribute: attribute})
			}
		}
	}
	for _, targets := range index {
		sort.Slice(targets, func(i, j int) bool { return targets[i].address < targets[j].address })
	}
	return index
}

// uniqueTarget picks, of the resources that have value, the one the
// argument refers to: the one resource, or the others' parent (an S3
// bucket, whose versioning and policy resources have the bucket's name as
// their id too), if the value identifies a resource on its own (see
// referenceable) or the argument is named after the parent's type; else
// the one resource whose type the argument is named after (role = "ci"
// names aws_iam_role.ci, not aws_iam_instance_profile.ci). It reports
// false when there is none.
func uniqueTarget(argument, value string, targets []referenceTarget) (referenceTarget, bool) {
	addresses := make([]string, 0, len(targets))
	for _, t := range targets {
		addresses = append(addresses, t.address)
	}
	if parent := parentOf(addresses); parent != "" && (referenceable(value) || namedAfter(argument, parent)) {
		for _, t := range targets {
			if t.address == parent {
				return t, true
			}
		}
	}
	if referenceable(value) {
		return referenceTarget{}, false
	}
	var named []referenceTarget
	for _, t := range targets {
		if namedAfter(argument, t.address) {
			named = append(named, t)
		}
	}
	if len(named) != 1 {
		return referenceTarget{}, false
	}
	return named[0], true
}

// parentOf returns the one address, or the address whose type starts every
// other address's type; "" if there is none.
func parentOf(addresses []string) string {
	if len(addresses) == 1 {
		return addresses[0]
	}
	sort.Strings(addresses)
	for _, candidate := range addresses {
		parentType := AddressType(candidate)
		isParent := true
		for _, other := range addresses {
			if other != candidate && !strings.HasPrefix(AddressType(other), parentType+"_") {
				isParent = false
				break
			}
		}
		if isParent {
			return candidate
		}
	}
	return ""
}

// referenceable reports whether a value identifies a resource on its own:
// an ARN, a URL (an SQS queue's id), a cloud ID such as vpc-0abc1234 or a
// Google service account's email, not a name another resource could share
// by chance.
func referenceable(value string) bool {
	return liftable.MatchString(value) || strings.HasPrefix(value, "https://") || strings.HasSuffix(value, ".gserviceaccount.com")
}

// namedAfter reports whether argument is named after the last word of the
// referenced resource's type: bucket for aws_s3_bucket, role for
// aws_iam_role, rule for aws_cloudwatch_event_rule.
func namedAfter(argument, address string) bool {
	typ := AddressType(address)
	return argument == typ[strings.LastIndex(typ, "_")+1:]
}

// resolver finds the references that replace literals, recording each in
// g, which rules out those that would close a cycle.
type resolver struct {
	index   map[string][]referenceTarget
	parents parentNames
	// ids, if set, finds what index and parents don't (see identities).
	ids *identities
	g   dependencies
	src []byte
}

// resolve returns the reference that replaces value, of the argument name
// of the resource at address, in its nested block block ("" at the top):
// one of index or parents, else, if all is set, one ids finds. preferred
// are the resources other arguments of the same block refer to, which
// settle a value several resources share.
func (r *resolver) resolve(address, name, block, value string, all bool, preferred map[string]bool) (replacement, bool) {
	t, ok := uniqueTarget(name, value, r.index[value])
	if !ok {
		t, ok = r.parents.parent(address, name, value)
	}
	if ok {
		if t.address == address || !r.g.add(address, t.node()) {
			return nil, false
		}
		return replacement{{ref: &t}}, true
	}
	if !all || r.ids == nil {
		return nil, false
	}
	rp, ok := r.ids.resolve(address, effectiveArgument(name, block), block == "", value, r.parents, preferred)
	if !ok || !r.addAll(address, rp) {
		return nil, false
	}
	return rp, true
}

// addAll records the references of rp from address, unless one would
// close a cycle or refer to address itself, and reports whether it did.
func (r *resolver) addAll(address string, rp replacement) bool {
	nodes := rp.nodes()
	for _, n := range nodes {
		if n == address || r.g.reaches(n, address, map[string]bool{}) {
			return false
		}
	}
	for _, n := range nodes {
		r.g.add(address, n)
	}
	return true
}

// unreferencedMaps are arguments whose maps hold no references: tags and
// labels describe a resource, even where a value is another's ID.
var unreferencedMaps = map[string]bool{"tags": true, "tags_all": true, "labels": true, "terraform_labels": true, "effective_labels": true, "default_tags": true}

// attributes replaces the literals in body, of the resource at address,
// in its nested block block ("" at the top), and in its nested blocks,
// with references (see resolve). With all set, it also looks in maps,
// such as an alarm's dimensions, by their keys, and gives an argument the
// reference another argument of the block got for the same ID, as an API
// Gateway method's cache_namespace, which is its resource_id. It reports
// whether it changed anything.
func (r *resolver) attributes(address string, syntax *hclsyntax.Body, body *hclwrite.Body, block string, all bool) bool {
	changed := false
	preferred := map[string]bool{}
	byValue := map[string]replacement{}
	var unresolved []string
	resolve := func(name, value string) (replacement, bool) {
		rp, ok := r.resolve(address, name, block, value, all, preferred)
		if ok {
			for _, n := range rp.nodes() {
				preferred[n] = true
			}
		}
		return rp, ok
	}
	tokens := func(name, value string) (hclwrite.Tokens, bool) {
		rp, ok := resolve(name, value)
		if !ok {
			return nil, false
		}
		return rp.tokens(), true
	}
	for _, name := range attributeNames(syntax) {
		switch expr := syntax.Attributes[name].Expr.(type) {
		case *hclsyntax.TemplateExpr:
			v, ok := literalString(expr)
			if !ok {
				continue
			}
			rp, ok := resolve(name, v)
			if !ok {
				unresolved = append(unresolved, name)
				continue
			}
			body.SetAttributeRaw(name, rp.tokens())
			changed = true
			if t, single := rp.single(); single && idAttribute(t.attribute) && distinctive(v) {
				byValue[v] = rp
			}
		case *hclsyntax.TupleConsExpr:
			if replaced, ok := referenceTuple(name, expr, tokens, r.src); ok {
				body.SetAttributeRaw(name, replaced)
				changed = true
			}
		case *hclsyntax.ObjectConsExpr:
			if !all || unreferencedMaps[name] {
				continue
			}
			byKey := func(key, value string) (hclwrite.Tokens, bool) { return tokens(snakeCase(key), value) }
			if replaced, ok := referenceObject(expr, byKey, r.src); ok {
				body.SetAttributeRaw(name, replaced)
				changed = true
			}
		}
	}
	if all {
		for _, name := range unresolved {
			v, _ := literalString(syntax.Attributes[name].Expr.(*hclsyntax.TemplateExpr))
			if rp, ok := byValue[v]; ok {
				body.SetAttributeRaw(name, rp.tokens())
				changed = true
			}
		}
	}
	blocks := body.Blocks()
	for i, b := range syntax.Blocks {
		if i < len(blocks) && r.attributes(address, b.Body, blocks[i].Body(), b.Type, all) {
			changed = true
		}
	}
	return changed
}

// attributeNames returns the names of body's attributes in the order they
// are written.
func attributeNames(body *hclsyntax.Body) []string {
	names := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return body.Attributes[names[i]].SrcRange.Start.Byte < body.Attributes[names[j]].SrcRange.Start.Byte
	})
	return names
}

// referenceTuple rewrites a list with the string literals that are
// references replaced, if any are, and those of its objects' attributes,
// such as a route table's routes. Other elements stay as written in src.
func referenceTuple(argument string, expr *hclsyntax.TupleConsExpr, target func(argument, value string) (hclwrite.Tokens, bool), src []byte) (hclwrite.Tokens, bool) {
	elems := make([]hclwrite.Tokens, 0, len(expr.Exprs))
	replaced := false
	for _, e := range expr.Exprs {
		if object, ok := e.(*hclsyntax.ObjectConsExpr); ok {
			if tokens, ok := referenceObject(object, target, src); ok {
				elems = append(elems, tokens)
				replaced = true
				continue
			}
		}
		if tmpl, ok := e.(*hclsyntax.TemplateExpr); ok {
			if v, ok := literalString(tmpl); ok {
				if tokens, ok := target(argument, v); ok {
					elems = append(elems, tokens)
					replaced = true
				} else {
					elems = append(elems, hclwrite.TokensForValue(cty.StringVal(v)))
				}
				continue
			}
		}
		rng := e.Range()
		if rng.End.Byte > len(src) || rng.Start.Byte > rng.End.Byte {
			return nil, false
		}
		elems = append(elems, hclwrite.Tokens{{Type: hclsyntax.TokenIdent, Bytes: rng.SliceBytes(src)}})
	}
	if !replaced {
		return nil, false
	}
	return hclwrite.TokensForTuple(elems), true
}

// referenceObject rewrites an object with the string literals of its
// attributes that are references replaced, if any are, each as the
// argument its key names. Other attributes stay as written in src.
func referenceObject(expr *hclsyntax.ObjectConsExpr, target func(argument, value string) (hclwrite.Tokens, bool), src []byte) (hclwrite.Tokens, bool) {
	attrs := make([]hclwrite.ObjectAttrTokens, 0, len(expr.Items))
	replaced := false
	asWritten := func(e hclsyntax.Expression) (hclwrite.Tokens, bool) {
		rng := e.Range()
		if rng.End.Byte > len(src) || rng.Start.Byte > rng.End.Byte {
			return nil, false
		}
		return hclwrite.Tokens{{Type: hclsyntax.TokenIdent, Bytes: rng.SliceBytes(src)}}, true
	}
	for _, item := range expr.Items {
		key, ok := asWritten(item.KeyExpr)
		if !ok {
			return nil, false
		}
		if tmpl, ok := item.ValueExpr.(*hclsyntax.TemplateExpr); ok {
			if v, ok := literalString(tmpl); ok {
				if tokens, ok := target(keyName(item.KeyExpr), v); ok {
					attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: key, Value: tokens})
					replaced = true
					continue
				}
			}
		}
		value, ok := asWritten(item.ValueExpr)
		if !ok {
			return nil, false
		}
		attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: key, Value: value})
	}
	if !replaced {
		return nil, false
	}
	return hclwrite.TokensForObject(attrs), true
}

// keyName returns an object key as written: an identifier, or a string.
func keyName(expr hclsyntax.Expression) string {
	if name := hcl.ExprAsKeyword(expr); name != "" {
		return name
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || !v.Type().Equals(cty.String) {
		return ""
	}
	return v.AsString()
}

func literalString(expr *hclsyntax.TemplateExpr) (string, bool) {
	if !expr.IsStringLiteral() {
		return "", false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.Type().Equals(cty.String) {
		return "", false
	}
	return v.AsString(), true
}

// dependencies are the references between resources, from referrer to
// referenced.
type dependencies map[string]map[string]bool

// add records that from refers to to, unless to already depends on from,
// directly or not, which would make a cycle. It reports whether it added
// the reference.
func (g dependencies) add(from, to string) bool {
	if g.reaches(to, from, map[string]bool{}) {
		return false
	}
	if g[from] == nil {
		g[from] = map[string]bool{}
	}
	g[from][to] = true
	return true
}

func (g dependencies) reaches(from, to string, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	for next := range g[from] {
		if g.reaches(next, to, seen) {
			return true
		}
	}
	return false
}

// parentNames finds the parent a resource names, such as the resource
// group of resource_group_name, by the name and the IDs: Azure's IDs are
// paths, and a child's starts with its parent's, so that a name another
// resource group shares by chance doesn't count.
type parentNames struct {
	// byName has the resources with a path for an ID, by name.
	byName map[string][]namedResource
	// ids has every resource's ID, by address.
	ids map[string]string
	// groups are the containers whose location what is in them shares,
	// such as Azure resource groups, with their locations (see
	// catalogue.Container).
	groups []namedResource
}

type namedResource struct {
	address, id string
	// location is a resource group's.
	location string
}

// parentIndex indexes the resources of values whose IDs are paths.
func parentIndex(values map[string]map[string]any) parentNames {
	p := parentNames{byName: map[string][]namedResource{}, ids: map[string]string{}}
	for address, attrs := range values {
		id, _ := attrs["id"].(string)
		if !strings.HasPrefix(id, "/") {
			continue
		}
		p.ids[address] = id
		if name, _ := attrs["name"].(string); name != "" {
			p.byName[name] = append(p.byName[name], namedResource{address: address, id: id})
		}
		if location, _ := attrs["location"].(string); location != "" && sharesLocation(AddressType(address)) {
			p.groups = append(p.groups, namedResource{address: address, id: id, location: location})
		}
	}
	return p
}

// location returns the resource group whose location value is, of the
// resource at address, if the resource is in the group: its ID starts
// with the group's, whatever the case. A resource elsewhere keeps its
// location, even where it is the same.
func (p parentNames) location(address, value string) (referenceTarget, bool) {
	own := strings.ToLower(p.ids[address])
	if own == "" {
		return referenceTarget{}, false
	}
	var found []namedResource
	for _, g := range p.groups {
		if g.address != address && g.location == value && strings.HasPrefix(own, strings.ToLower(g.id)+"/") {
			found = append(found, g)
		}
	}
	if len(found) != 1 {
		return referenceTarget{}, false
	}
	return referenceTarget{address: found[0].address, attribute: "location"}, true
}

// parent returns the resource an argument <type>_name of the resource at
// address names: the one resource of a type ending in <type> with that
// name whose ID the resource's ID starts with, whatever the case.
func (p parentNames) parent(address, argument, value string) (referenceTarget, bool) {
	kind, ok := strings.CutSuffix(argument, "_name")
	own := strings.ToLower(p.ids[address])
	if !ok || kind == "" || own == "" {
		return referenceTarget{}, false
	}
	var found []namedResource
	for _, r := range p.byName[value] {
		if r.address != address && strings.HasSuffix(AddressType(r.address), "_"+kind) && strings.HasPrefix(own, strings.ToLower(r.id)+"/") {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		return referenceTarget{}, false
	}
	return referenceTarget{address: found[0].address, attribute: "name"}, true
}

// sharesLocation reports whether resources of typ hold others that share
// their location unless they set their own, as the catalogue says (see
// catalogue.Container), such as Azure resource groups.
func sharesLocation(typ string) bool {
	c, ok := catalogue.ForType(typ).Container(typ)
	return ok && c.Location
}
