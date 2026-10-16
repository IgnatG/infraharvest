// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// part is a piece of what a literal becomes: text, or a reference.
type part struct {
	text string
	ref  *referenceTarget
}

// replacement is what a literal becomes: a reference, or a string
// template of references and text, such as
// "${aws_apigatewayv2_api.http.execution_arn}/*".
type replacement []part

// single returns the reference rp is, if it is one alone.
func (rp replacement) single() (referenceTarget, bool) {
	if len(rp) != 1 || rp[0].ref == nil {
		return referenceTarget{}, false
	}
	return *rp[0].ref, true
}

// nodes are the nodes of the dependency graph rp refers to (see
// referenceTarget.node).
func (rp replacement) nodes() []string {
	var nodes []string
	for _, p := range rp {
		if p.ref != nil && !slices.Contains(nodes, p.ref.node()) {
			nodes = append(nodes, p.ref.node())
		}
	}
	return nodes
}

// tokens renders rp: a reference alone, or a quoted template.
func (rp replacement) tokens() hclwrite.Tokens {
	if t, ok := rp.single(); ok {
		return hclwrite.TokensForTraversal(t.traversal())
	}
	tokens := hclwrite.Tokens{{Type: hclsyntax.TokenOQuote, Bytes: []byte(`"`)}}
	for _, p := range rp {
		if p.ref == nil {
			if p.text == "" {
				continue
			}
			// The value's tokens without its quotes: TokensForValue escapes
			// the text, as ${ and %{ in it.
			lit := hclwrite.TokensForValue(cty.StringVal(p.text))
			tokens = append(tokens, lit[1:len(lit)-1]...)
			continue
		}
		tokens = append(tokens, &hclwrite.Token{Type: hclsyntax.TokenTemplateInterp, Bytes: []byte("${")})
		tokens = append(tokens, hclwrite.TokensForTraversal(p.ref.traversal())...)
		tokens = append(tokens, &hclwrite.Token{Type: hclsyntax.TokenTemplateSeqEnd, Bytes: []byte("}")})
	}
	return append(tokens, &hclwrite.Token{Type: hclsyntax.TokenCQuote, Bytes: []byte(`"`)})
}

// identity is a value that identifies a resource: one of its attributes,
// or a template of several.
type identity struct {
	address, attribute string
	// named is set when the value is the resource's name, which another
	// resource may have by chance, even in an attribute such as id.
	named bool
	// parts, if set, make the value from attributes of the resource, as
	// an ECS task definition's family:revision.
	parts replacement
}

func (id identity) replacement() replacement {
	if id.parts != nil {
		return id.parts
	}
	return replacement{{ref: &referenceTarget{address: id.address, attribute: id.attribute}}}
}

// computedIdentities are the attributes, besides the ones every resource
// has, that identify the resource that has them where the provider
// computes them: an API's root resource and execution ARN, a KMS key's
// key ID, a hosted zone's ID, a load balancer's DNS name and zone, the
// domain names and zones of a distribution and a bucket, a
// managed identity's principal. Terraform writes every argument into the
// configuration it generates, so an attribute it didn't write is computed
// only, as the schema says; one it wrote names something else, such as a
// role assignment's principal_id.
var computedIdentities = []string{"execution_arn", "root_resource_id", "key_id", "zone_id", "hosted_zone_id", "dns_name", "domain_name", "bucket_regional_domain_name", "bucket_domain_name", "fqdn", "arn_suffix", "principal_id", "client_id", "unique_id"}

// identityAttributes are the attributes identityIndex indexes, in the
// order a reference prefers them when a resource has the same value in
// several.
var identityAttributes = append(append([]string{"arn", "self_link"}, computedIdentities...), "name", "function_name", "bucket", "id")

// nameAttributes hold names, which resources of other types may share.
var nameAttributes = map[string]bool{"name": true, "function_name": true, "bucket": true}

// sharedAttributes hold values many resources have without being them:
// where a resource's identity is one of them, as the region of a
// region-wide setting, it identifies nothing.
var sharedAttributes = []string{"region", "location", "project", "account_id", "owner_id", "tenant_id", "subscription_id"}

// regionLike matches cloud regions and zones, such as eu-west-2 and
// us-central1-a, which some region-wide settings have as their ID.
var regionLike = regexp.MustCompile(`^[a-z]{2,}(-[a-z]+)+-?[0-9]+[a-z]?$`)

// derivedIdentities make, by resource type, an identity of several
// attributes, as Terraform reads it back in other resources.
var derivedIdentities = map[string]func(address string, attrs map[string]any) (string, replacement, bool){
	// An ECS service's task_definition reads back as family:revision,
	// unless it is set to the ARN: a reference to the ARN would plan an
	// update.
	"aws_ecs_task_definition": func(address string, attrs map[string]any) (string, replacement, bool) {
		family, _ := attrs["family"].(string)
		revision, ok := numberString(attrs["revision"])
		if family == "" || !ok {
			return "", nil, false
		}
		return family + ":" + revision, replacement{
			{ref: &referenceTarget{address: address, attribute: "family"}},
			{text: ":"},
			{ref: &referenceTarget{address: address, attribute: "revision"}},
		}, true
	},
}

// numberString formats a whole number of a plan's JSON.
func numberString(v any) (string, bool) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) {
			return "", false
		}
		return strconv.FormatInt(int64(n), 10), true
	case json.Number:
		return n.String(), true
	case int:
		return strconv.Itoa(n), true
	}
	return "", false
}

// identities find what a literal refers to that referenceIndex and
// parentNames don't:
//
//   - a resource's short ID or name, where the argument is named after its
//     type (rest_api_id, log_group, a dimension's QueueName, target_key_id
//     for a KMS key's key_id), or its ID is distinctive and the argument
//     takes IDs (an API's root resource as a parent_id);
//   - the other attributes that identify a resource (see
//     computedIdentities), and an ECS task definition's family:revision;
//   - IDs and ARNs inside a longer string, whole between its / and :
//     separators: "integrations/${aws_apigatewayv2_integration.x.id}";
//   - an Azure resource's location, as its resource group's (see
//     parentNames.location).
//
// A value several resources have refers to none of them, unless one is
// the others' parent, or the one another argument of the block refers to.
type identities struct {
	byValue map[string][]identity
}

// identityIndex indexes the identities of the resources of values, with
// the arguments written in their configuration, by address.
func identityIndex(values map[string]map[string]any, written map[string]map[string]bool) *identities {
	x := &identities{byValue: map[string][]identity{}}
	addresses := make([]string, 0, len(values))
	for address := range values {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	for _, address := range addresses {
		attrs := values[address]
		shared := map[string]bool{}
		for _, key := range sharedAttributes {
			if v, _ := attrs[key].(string); v != "" {
				shared[v] = true
			}
		}
		for _, key := range identityAttributes {
			v, _ := attrs[key].(string)
			if v == "" || shared[v] || regionLike.MatchString(v) || (slices.Contains(computedIdentities, key) && written[address][key]) {
				continue
			}
			named := nameAttributes[key] || attrs["name"] == v || attrs["function_name"] == v || attrs["bucket"] == v || attrs["family"] == v
			x.byValue[v] = append(x.byValue[v], identity{address: address, attribute: key, named: named})
		}
		if derive, ok := derivedIdentities[resourceTypeOf(address)]; ok {
			if v, parts, ok := derive(address, attrs); ok {
				x.byValue[v] = append(x.byValue[v], identity{address: address, named: true, parts: parts})
			}
		}
	}
	return x
}

// writtenArguments returns the arguments and nested blocks each resource
// of f sets, by address.
func writtenArguments(f *hclFile) map[string]map[string]bool {
	written := map[string]map[string]bool{}
	for _, r := range f.resources() {
		set := map[string]bool{}
		for name := range r.syntax.Body.Attributes {
			set[name] = true
		}
		for _, b := range r.syntax.Body.Blocks {
			set[b.Type] = true
		}
		written[r.address] = set
	}
	return written
}

// resolve returns what value, of the argument (see effectiveArgument) of
// the resource at address, refers to, at the top of the resource or not.
func (x *identities) resolve(address, argument string, top bool, value string, parents parentNames, preferred map[string]bool) (replacement, bool) {
	if top && argument == "location" {
		if t, ok := parents.location(address, value); ok {
			return replacement{{ref: &t}}, true
		}
		return nil, false
	}
	if rp, ok := x.whole(address, argument, value, preferred); ok {
		return rp, true
	}
	return x.embedded(address, value)
}

// whole returns the resource value identifies, whole: of the resources
// other than address's that have it, the one the argument is named after,
// or, where the argument takes IDs and the value is distinctive, the one
// whose ID it is; or, for a DNS name or an ARN suffix (an alarm's
// LoadBalancer dimension), the one with it.
func (x *identities) whole(address, argument, value string, preferred map[string]bool) (replacement, bool) {
	byAddress := map[string][]identity{}
	var candidates []string
	for _, id := range x.byValue[value] {
		if id.address == address {
			continue
		}
		if _, seen := byAddress[id.address]; !seen {
			candidates = append(candidates, id.address)
		}
		byAddress[id.address] = append(byAddress[id.address], id)
	}
	if len(candidates) == 0 {
		return nil, false
	}
	choose := func(addresses []string, usable func(identity) bool) (replacement, bool) {
		addresses = slices.DeleteFunc(slices.Clone(addresses), func(a string) bool { return !slices.ContainsFunc(byAddress[a], usable) })
		chosen, ok := oneOf(addresses, preferred)
		if !ok {
			return nil, false
		}
		var ids []identity
		for _, id := range byAddress[chosen] {
			if usable(id) {
				ids = append(ids, id)
			}
		}
		return bestIdentity(argument, ids).replacement(), true
	}
	var named []string
	for _, a := range candidates {
		if namesType(argument, a) {
			named = append(named, a)
		}
	}
	switch {
	case len(named) > 0:
		return choose(named, func(identity) bool { return true })
	case takesIDs(argument) && distinctive(value):
		return choose(candidates, func(id identity) bool { return !id.named })
	case strings.ContainsAny(value, "./"):
		return choose(candidates, func(id identity) bool { return dnsAttribute(id.attribute) || id.attribute == "arn_suffix" })
	}
	return nil, false
}

// containerTypes hold other resources: their IDs start the IDs of what is
// in them, which reads better whole than as "${...}/providers/...".
var containerTypes = map[string]bool{"azurerm_resource_group": true, "google_project": true, "google_folder": true, "google_organization": true}

// embedded returns value with the IDs and ARNs inside it replaced by
// references: each a run of whole segments between its / and :
// separators, the longest first, that is the distinctive identity of one
// resource other than address's. Values with spaces, such as
// descriptions, stay.
func (x *identities) embedded(address, value string) (replacement, bool) {
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return nil, false
	}
	starts, ends := []int{0}, []int{}
	for i := 0; i < len(value); i++ {
		if value[i] == '/' || value[i] == ':' {
			ends = append(ends, i)
			starts = append(starts, i+1)
		}
	}
	ends = append(ends, len(value))
	var rp replacement
	text := 0 // where the text not yet in rp starts
	for _, start := range starts {
		if start < text {
			continue
		}
		for i := len(ends) - 1; i >= 0; i-- {
			end := ends[i]
			if end <= start || (start == 0 && end == len(value)) {
				continue
			}
			sub := value[start:end]
			if !distinctive(sub) {
				continue
			}
			ref, ok := x.embeddable(address, sub)
			if !ok {
				continue
			}
			rp = append(rp, part{text: value[text:start]}, part{ref: &ref})
			text = end
			break
		}
	}
	if rp == nil {
		return nil, false
	}
	return append(rp, part{text: value[text:]}), true
}

// embeddable returns the attribute value is of one resource other than
// address's, if it is an ID rather than a name.
func (x *identities) embeddable(address, value string) (referenceTarget, bool) {
	byAddress := map[string]identity{}
	var addresses []string
	for _, id := range x.byValue[value] {
		if id.address == address || id.named || id.parts != nil || containerTypes[resourceTypeOf(id.address)] {
			continue
		}
		if _, seen := byAddress[id.address]; !seen {
			byAddress[id.address] = id
			addresses = append(addresses, id.address)
		}
	}
	chosen, ok := oneOf(addresses, nil)
	if !ok {
		return referenceTarget{}, false
	}
	return referenceTarget{address: chosen, attribute: byAddress[chosen].attribute}, true
}

// oneOf returns the one address of addresses, the others' parent (see
// parentOf), or the one of them in preferred.
func oneOf(addresses []string, preferred map[string]bool) (string, bool) {
	switch len(addresses) {
	case 0:
		return "", false
	case 1:
		return addresses[0], true
	}
	if parent := parentOf(slices.Clone(addresses)); parent != "" {
		return parent, true
	}
	var in []string
	for _, a := range addresses {
		if preferred[a] {
			in = append(in, a)
		}
	}
	if len(in) == 1 {
		return in[0], true
	}
	return "", false
}

// bestIdentity returns the identity of ids the argument names, as
// target_key_id names a key's key_id, else the first.
func bestIdentity(argument string, ids []identity) identity {
	best, length := ids[0], 0
	for _, id := range ids {
		if id.attribute != "" && len(id.attribute) > length && (argument == id.attribute || strings.HasSuffix(argument, "_"+id.attribute)) {
			best, length = id, len(id.attribute)
		}
	}
	return best
}

// idSuffixes end arguments that hold IDs, ARNs or names of other
// resources.
var idSuffixes = []string{"_ids", "_id", "_arns", "_arn", "_names", "_name"}

// namesType reports whether argument names the type of the resource at
// address: it is, without a suffix such as _id or a plural s, the end of
// the type, as rest_api_id names aws_api_gateway_rest_api and log_group
// aws_cloudwatch_log_group, or it ends with the type's last word, as
// target_key_id names aws_kms_key.
func namesType(argument, address string) bool {
	words := strings.Split(resourceTypeOf(address), "_")
	if len(words) < 2 || argument == "" {
		return false
	}
	words = words[1:] // without the provider
	bases := []string{argument}
	for _, suffix := range idSuffixes {
		if base, ok := strings.CutSuffix(argument, suffix); ok && base != "" {
			bases = append(bases, base)
			break
		}
	}
	for _, base := range bases {
		for _, b := range []string{base, strings.TrimSuffix(base, "s")} {
			for i := range words {
				if strings.Join(words[i:], "_") == b {
					return true
				}
			}
		}
	}
	return len(bases) > 1 && strings.HasSuffix(bases[1], "_"+words[len(words)-1])
}

// takesIDs reports whether argument holds IDs or ARNs.
func takesIDs(argument string) bool {
	for _, suffix := range []string{"_id", "_ids", "_arn", "_arns"} {
		if strings.HasSuffix(argument, suffix) {
			return true
		}
	}
	return false
}

// dnsAttribute reports whether attribute holds a DNS name, which only the
// resource that has it has: a load balancer's dns_name, a bucket's
// bucket_regional_domain_name.
func dnsAttribute(attribute string) bool {
	return attribute == "dns_name" || attribute == "fqdn" || strings.HasSuffix(attribute, "domain_name")
}

// idAttribute reports whether attribute holds an ID or ARN.
func idAttribute(attribute string) bool {
	return attribute == "id" || attribute == "arn" || takesIDs(attribute)
}

// distinctive reports whether value identifies a resource on its own
// (see referenceable), or looks like a generated ID: six characters or
// more, letters and digits, no spaces.
func distinctive(value string) bool {
	if referenceable(value) {
		return true
	}
	return len(value) >= 6 && strings.ContainsFunc(value, unicode.IsLetter) && strings.ContainsFunc(value, unicode.IsDigit) && !strings.ContainsFunc(value, unicode.IsSpace)
}

// effectiveArgument names the argument a literal is in: name, or, for the
// name, id or ARN of a nested block, the block's with it, as
// iam_instance_profile_name for the name of an iam_instance_profile block.
func effectiveArgument(name, block string) string {
	if block != "" && (name == "name" || name == "id" || name == "arn") {
		return block + "_" + name
	}
	return name
}

// snakeCase turns a map key such as QueueName or DBInstanceIdentifier into
// queue_name or db_instance_identifier, as arguments are named.
func snakeCase(key string) string {
	runes := []rune(key)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteRune('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.ReplaceAll(b.String(), "-", "_")
}
