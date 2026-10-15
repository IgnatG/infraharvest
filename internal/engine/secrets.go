// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// Secret is a variable the generated configuration reads a secret value
// from. Terraform doesn't write secret values into the configuration it
// generates, so the user sets the variable before planning.
type Secret struct {
	Variable  string // variable name
	Address   string // address of the resource that uses it
	Attribute string // attribute it sets, e.g. value or block[1].password

	schemaPath []string // block types and attribute name, e.g. [block password]
	ty         cty.Type // from the provider schema; cty.NilType if unknown
}

// secretAttribute is an attribute Terraform generated as
// "null # sensitive": it has a value Terraform won't show.
type secretAttribute struct {
	address    string
	path       string   // e.g. value or block[1].password
	schemaPath []string // block types and attribute name, e.g. [block password]
	body       *hclwrite.Body
	rng        hcl.Range
}

func (s secretAttribute) name() string {
	return s.schemaPath[len(s.schemaPath)-1]
}

// findSecrets lists the secret attributes of every resource in f, by
// resource address.
func findSecrets(f *hclFile) map[string][]secretAttribute {
	secrets := map[string][]secretAttribute{}
	for _, r := range f.resources() {
		if found := secretsIn(r.address, r.syntax.Body, r.write.Body(), "", nil); len(found) > 0 {
			secrets[r.address] = found
		}
	}
	return secrets
}

func secretsIn(address string, syntax *hclsyntax.Body, body *hclwrite.Body, prefix string, schemaPrefix []string) []secretAttribute {
	var found []secretAttribute
	names := make([]string, 0, len(syntax.Attributes))
	for name := range syntax.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if attr := body.GetAttribute(name); attr != nil && isSecretNull(attr) {
			found = append(found, secretAttribute{
				address:    address,
				path:       prefix + name,
				schemaPath: append(append([]string(nil), schemaPrefix...), name),
				body:       body,
				rng:        syntax.Attributes[name].SrcRange,
			})
		}
	}
	blocks := body.Blocks()
	count := map[string]int{}
	for _, b := range syntax.Blocks {
		count[b.Type]++
	}
	index := map[string]int{}
	for i, b := range syntax.Blocks {
		if i >= len(blocks) {
			break
		}
		segment := b.Type
		if count[b.Type] > 1 {
			segment += "[" + strconv.Itoa(index[b.Type]) + "]"
		}
		index[b.Type]++
		schemaPath := append(append([]string(nil), schemaPrefix...), b.Type)
		found = append(found, secretsIn(address, b.Body, blocks[i].Body(), prefix+segment+".", schemaPath)...)
	}
	return found
}

// isSecretNull reports whether attr is written "null # sensitive", which is
// how Terraform generates attributes the provider marks sensitive.
func isSecretNull(attr *hclwrite.Attribute) bool {
	var expr []string
	for _, t := range attr.Expr().BuildTokens(nil) {
		if t.Type != hclsyntax.TokenNewline && t.Type != hclsyntax.TokenComment {
			expr = append(expr, string(t.Bytes))
		}
	}
	if len(expr) != 1 || expr[0] != "null" {
		return false
	}
	for _, t := range attr.BuildTokens(nil) {
		if t.Type == hclsyntax.TokenComment && strings.TrimSpace(string(t.Bytes)) == "# sensitive" {
			return true
		}
	}
	return false
}

// aboutSecret reports whether d is about one of secrets: its range is on
// the attribute, or its message names the attribute, as in
// "one of insecure_value,value,value_wo must be specified". Such errors are
// expected while the attributes are null and go away once they read from
// variables; validation after that catches any taken for one by mistake.
func aboutSecret(d tfjson.Diagnostic, secrets []secretAttribute) bool {
	words := map[string]bool{}
	for _, w := range notInIdentifier.Split(d.Summary+" "+d.Detail, -1) {
		words[w] = true
	}
	for _, s := range secrets {
		if d.Range != nil && spans(s.rng, d.Range.Start.Line) || words[s.name()] {
			return true
		}
	}
	return false
}

var notInIdentifier = regexp.MustCompile(`[^A-Za-z0-9_]+`)

var notInVariableName = regexp.MustCompile(`[^A-Za-z0-9]+`)

// secretsToVariables makes every secret attribute read from a new sensitive
// variable, named other than the variables in taken, and returns the
// variables, by resource in file order and by attribute as secretsIn
// lists them (by name, then nested blocks in order). It edits f's tree.
func secretsToVariables(f *hclFile, secrets map[string][]secretAttribute, taken Names) []Secret {
	var vars []Secret
	used := map[string]bool{}
	for name := range taken {
		if v, ok := strings.CutPrefix(name, "var."); ok {
			used[v] = true
		}
	}
	for _, r := range f.resources() {
		for _, s := range secrets[r.address] {
			// Shells can only set TF_VAR_<name> for names without dashes.
			base := strings.Trim(notInVariableName.ReplaceAllString(s.address+"_"+s.path, "_"), "_")
			name := base
			for i := 2; used[name]; i++ {
				name = fmt.Sprintf("%s_%d", base, i)
			}
			used[name] = true
			s.body.SetAttributeTraversal(s.name(), hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: name}})
			vars = append(vars, Secret{Variable: name, Address: s.address, Attribute: s.path, schemaPath: s.schemaPath})
		}
	}
	return vars
}

// variablesFile renders a sensitive variable without a default for each
// secret, typed from the provider schemas where they describe it.
func variablesFile(secrets []Secret, schemas *tfjson.ProviderSchemas) ([]byte, error) {
	f := hclwrite.NewEmptyFile()
	body := f.Body()
	for i, s := range secrets {
		if i > 0 {
			body.AppendNewline()
		}
		b := body.AppendNewBlock("variable", []string{s.Variable}).Body()
		b.SetAttributeValue("description", cty.StringVal(fmt.Sprintf(
			"%s of %s. Terraform doesn't write secret values into the configuration it generates: set it before planning, for example in a .tfvars file kept out of version control.",
			s.Attribute, s.Address)))
		if ty := attributeType(schemas, strings.SplitN(s.Address, ".", 2)[0], s.schemaPath); ty != cty.NilType {
			tokens, err := typeTokens(ty)
			if err != nil {
				return nil, fmt.Errorf("variable %s: %w", s.Variable, err)
			}
			b.SetAttributeRaw("type", tokens)
		}
		b.SetAttributeValue("sensitive", cty.True)
	}
	return hclwrite.Format(f.Bytes()), nil
}

// withoutWriteOnly leaves out write-only attributes (value_wo, for
// example). Terraform also generates them as "null # sensitive", but they
// are never stored, so null, unset, is what an import should produce.
func withoutWriteOnly(secrets map[string][]secretAttribute, schemas *tfjson.ProviderSchemas) map[string][]secretAttribute {
	kept := map[string][]secretAttribute{}
	for addr, attrs := range secrets {
		resourceType := strings.SplitN(addr, ".", 2)[0]
		for _, s := range attrs {
			if attr := schemaAttribute(schemas, resourceType, s.schemaPath); attr == nil || !attr.WriteOnly {
				kept[addr] = append(kept[addr], s)
			}
		}
	}
	return kept
}

// attributeType returns the type of the attribute at path in resourceType's
// schema, or cty.NilType if the schemas don't describe it.
func attributeType(schemas *tfjson.ProviderSchemas, resourceType string, path []string) cty.Type {
	if attr := schemaAttribute(schemas, resourceType, path); attr != nil {
		return attr.AttributeType
	}
	return cty.NilType
}

// schemaAttribute returns the attribute at path in resourceType's schema,
// or nil if the schemas don't describe it.
func schemaAttribute(schemas *tfjson.ProviderSchemas, resourceType string, path []string) *tfjson.SchemaAttribute {
	if schemas == nil || len(path) == 0 {
		return nil
	}
	for _, provider := range schemas.Schemas {
		resource, ok := provider.ResourceSchemas[resourceType]
		if !ok || resource.Block == nil {
			continue
		}
		block := resource.Block
		for _, name := range path[:len(path)-1] {
			nested, ok := block.NestedBlocks[name]
			if !ok || nested.Block == nil {
				return nil
			}
			block = nested.Block
		}
		return block.Attributes[path[len(path)-1]]
	}
	return nil
}

// typeTokens renders ty as a type constraint expression.
func typeTokens(ty cty.Type) (hclwrite.Tokens, error) {
	f, diags := hclwrite.ParseConfig([]byte("type = "+typeexpr.TypeString(ty)+"\n"), "type", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	return f.Body().GetAttribute("type").Expr().BuildTokens(nil), nil
}
