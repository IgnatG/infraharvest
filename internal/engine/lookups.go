// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// lookup is a data source that reads a value no root manages, which the
// cloud or the provider defines: the tenant Azure signs in to, a
// CloudFront policy or KMS key AWS manages.
type lookup struct {
	// typ and name are the data source's type and name, name unless
	// taken.
	typ, name string
	// args are its arguments, as name and value.
	args [][2]string
	// attribute is the data source's attribute that has the value.
	attribute string
}

func (l lookup) key() string {
	key := l.typ
	for _, a := range l.args {
		key += "\x00" + a[0] + "=" + a[1]
	}
	return key
}

// uuid matches a UUID, such as an Azure tenant's ID.
var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// kmsKeyARN matches the ARN of a KMS key.
var kmsKeyARN = regexp.MustCompile(`^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/[0-9a-f-]{36}$`)

// managedPolicy is a kind of CloudFront policy: the data source that reads
// one by name, and the IDs and names of those AWS manages, which are the
// same in every account.
type managedPolicy struct {
	dataType string
	names    map[string]string
}

// managedPolicies are the policies AWS manages, by the argument that takes
// their IDs. An ID missing here stays a literal; one that names another
// policy than its data source reads would change the plan, which keeps
// the literal.
var managedPolicies = map[string]managedPolicy{
	"cache_policy_id": {"aws_cloudfront_cache_policy", map[string]string{
		"658327ea-f89d-4fab-a63d-7e88639e58f6": "Managed-CachingOptimized",
		"4135ea2d-6df8-44a3-9df3-4b5a84be39ad": "Managed-CachingDisabled",
		"b2884449-e4de-46a7-ac36-70bc7f1ddd6d": "Managed-CachingOptimizedForUncompressedObjects",
		"08627262-05a9-4f76-9ded-b50ca2e3a84f": "Managed-Elemental-MediaPackage",
		"2e54312d-136d-493c-8eb9-b001f22f67d2": "Managed-Amplify",
	}},
	"origin_request_policy_id": {"aws_cloudfront_origin_request_policy", map[string]string{
		"216adef6-5c7f-47e4-b989-5492eafa07d3": "Managed-AllViewer",
		"b689b0a8-53d0-40ab-baf2-68738e2966ac": "Managed-AllViewerExceptHostHeader",
		"33f36d7e-f396-46d9-90e0-52428a34d9dc": "Managed-AllViewerAndCloudFrontHeaders-2022-06",
		"59781a5b-3903-41f3-afcb-af62929ccde1": "Managed-CORS-CustomOrigin",
		"88a5eaf4-2fd4-4709-b370-b4c650ea3fcf": "Managed-CORS-S3Origin",
		"acba4595-bd28-49b8-b9fe-13317c0390fa": "Managed-UserAgentRefererHeaders",
		"775133bc-15f2-49f9-abea-afb2e0bf67d2": "Managed-Elemental-MediaTailor-PersonalizedManifests",
	}},
	"response_headers_policy_id": {"aws_cloudfront_response_headers_policy", map[string]string{
		"60669652-455b-4ae9-85a4-c4c02393f86c": "Managed-SimpleCORS",
		"5cc3b908-e619-4b99-88e5-2cf7f45965bd": "Managed-CORS-With-Preflight",
		"67f7725c-6f97-4210-82d7-5512b31e9d03": "Managed-SecurityHeadersPolicy",
		"e61eb60c-9c35-4d20-a928-2b84e02af89c": "Managed-CORS-and-SecurityHeadersPolicy",
		"eaab4381-ed33-4a86-88ca-d9558dc6cd63": "Managed-CORS-with-preflight-and-SecurityHeadersPolicy",
	}},
}

// encryptedVolumes are the types whose kms_key_id encrypts EBS volumes.
var encryptedVolumes = map[string]bool{"aws_instance": true, "aws_ebs_volume": true, "aws_launch_template": true}

// lookupFor returns the data source that reads value, of the argument of
// a resource of resourceType, if one does.
func lookupFor(resourceType, argument, value string) (lookup, bool) {
	// The tenant the provider signs in to; the plan checks it is the
	// resource's.
	if strings.HasPrefix(resourceType, "azurerm_") && argument == "tenant_id" && uuid.MatchString(value) {
		return lookup{typ: "azurerm_client_config", name: "current", attribute: "tenant_id"}, true
	}
	if p, ok := managedPolicies[argument]; ok && strings.HasPrefix(resourceType, "aws_cloudfront_") {
		if name, ok := p.names[value]; ok {
			return lookup{typ: p.dataType, name: Label(snakeCase(strings.TrimPrefix(name, "Managed-"))), args: [][2]string{{"name", name}}, attribute: "id"}, true
		}
	}
	// A key no root manages, as the AWS-managed key of EBS encryption:
	// the plan checks it is that key.
	if encryptedVolumes[resourceType] && argument == "kms_key_id" && kmsKeyARN.MatchString(value) {
		return lookup{typ: "aws_kms_alias", name: "ebs", args: [][2]string{{"name", "alias/aws/ebs"}}, attribute: "target_key_arn"}, true
	}
	return lookup{}, false
}

// dataBlocks names the data sources of lookups, other than those in
// taken and data.tf, and keeps the blocks to add to data.tf.
type dataBlocks struct {
	path      string
	used      map[string]bool
	addresses map[string]string // by lookup key
	blocks    []*hclwrite.Block
}

func newDataBlocks(dir string, taken Names) (*dataBlocks, error) {
	d := &dataBlocks{path: filepath.Join(dir, DataFileName), used: map[string]bool{}, addresses: map[string]string{}}
	data, err := loadOptionalHCL(d.path)
	if err != nil {
		return nil, err
	}
	for name := range taken {
		if address, ok := strings.CutPrefix(name, "data."); ok {
			d.used[address] = true
		}
	}
	if data != nil {
		for _, b := range data.syntax.Blocks {
			if b.Type == "data" && len(b.Labels) == 2 {
				d.used[b.Labels[0]+"."+b.Labels[1]] = true
			}
		}
	}
	return d, nil
}

// reference returns the reference to l's attribute, adding its data
// source the first time.
func (d *dataBlocks) reference(l lookup) hclwrite.Tokens {
	address, ok := d.addresses[l.key()]
	if !ok {
		base := l.typ + "." + l.name
		address = base
		for n := 2; d.used[address]; n++ {
			address = fmt.Sprintf("%s_%d", base, n)
		}
		d.used[address] = true
		d.addresses[l.key()] = address
		typ, name, _ := strings.Cut(address, ".")
		block := hclwrite.NewBlock("data", []string{typ, name})
		for _, arg := range l.args {
			block.Body().SetAttributeValue(arg[0], cty.StringVal(arg[1]))
		}
		d.blocks = append(d.blocks, block)
	}
	typ, name, _ := strings.Cut(address, ".")
	return hclwrite.TokensForTraversal(hcl.Traversal{
		hcl.TraverseRoot{Name: "data"}, hcl.TraverseAttr{Name: typ}, hcl.TraverseAttr{Name: name}, hcl.TraverseAttr{Name: l.attribute},
	})
}

// write adds the data sources to data.tf.
func (d *dataBlocks) write() error {
	return appendBlocks(d.path, dataFileHeader, d.blocks)
}

// addLookups replaces literals in generated.tf that a data source reads
// (see lookupFor) with references to it, such as tenant_id =
// data.azurerm_client_config.current.tenant_id, for the resources not in
// skip, and adds the data sources to data.tf, named other than those in
// taken and data.tf. It returns the resources it changed.
func addLookups(dir string, taken Names, skip map[string]int) (map[string]bool, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	data, err := newDataBlocks(dir, taken)
	if err != nil {
		return nil, err
	}
	edited := map[string]bool{}
	for _, r := range generated.resources() {
		if skip[r.address] > 0 {
			continue
		}
		typ := resourceTypeOf(r.address)
		replace := func(argument, _, value string) (hclwrite.Tokens, bool) {
			l, ok := lookupFor(typ, argument, value)
			if !ok {
				return nil, false
			}
			return data.reference(l), true
		}
		if eachLiteral(r.syntax.Body, r.write.Body(), "", generated.src, replace) {
			edited[r.address] = true
		}
	}
	if len(edited) == 0 {
		return nil, nil
	}
	if err := generated.save(); err != nil {
		return nil, err
	}
	return edited, data.write()
}

// eachLiteral replaces the string literals of body, in nested block block
// ("" at the top), its lists and its nested blocks, with what replace
// returns for them, given the argument that holds each, and reports
// whether it replaced any.
func eachLiteral(syntax *hclsyntax.Body, body *hclwrite.Body, block string, src []byte, replace func(argument, block, value string) (hclwrite.Tokens, bool)) bool {
	changed := false
	for _, name := range attributeNames(syntax) {
		switch expr := syntax.Attributes[name].Expr.(type) {
		case *hclsyntax.TemplateExpr:
			if v, ok := literalString(expr); ok {
				if tokens, ok := replace(name, block, v); ok {
					body.SetAttributeRaw(name, tokens)
					changed = true
				}
			}
		case *hclsyntax.TupleConsExpr:
			inList := func(argument, value string) (hclwrite.Tokens, bool) { return replace(argument, block, value) }
			if tokens, ok := referenceTuple(name, expr, inList, src); ok {
				body.SetAttributeRaw(name, tokens)
				changed = true
			}
		}
	}
	blocks := body.Blocks()
	for i, b := range syntax.Blocks {
		if i < len(blocks) && eachLiteral(b.Body, blocks[i].Body(), b.Type, src, replace) {
			changed = true
		}
	}
	return changed
}
