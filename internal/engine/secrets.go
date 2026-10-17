// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// Secret is a variable the generated configuration reads a secret value
// from. Terraform doesn't write the values the provider marks sensitive
// into the configuration it generates, and infraharvest moves the secrets
// it finds among the others out of it (see literalSecretsToVariables), so
// the user sets the variable before planning.
type Secret struct {
	Variable  string // variable name
	Address   string // address of the resource that uses it
	Attribute string // attribute it sets, e.g. value, block[1].password or environment.variables.API_SECRET

	schemaPath []string // block types and attribute name, e.g. [block password]
	ty         cty.Type // from the provider schema; cty.NilType if unknown
	// key is the name a secret infraharvest found goes by, such as the
	// environment variable API_SECRET, and value its value, which only
	// infraharvest's own plans see (see secretValues); both are "" for
	// values the provider marks sensitive, which Terraform doesn't show.
	key, value string
}

// secretStoreAdvice ends the description of every secret variable.
const secretStoreAdvice = "set this variable from your secret store, such as AWS Secrets Manager, Azure Key Vault or Google Secret Manager, or with TF_VAR_%s, and keep the value out of version control"

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

// withoutUnset leaves out of found, and removes from their resources, the
// secret attributes the imported objects hold no value for, such as a
// database's master password, which AWS never returns: unset, the
// argument plans as the imported null, where a variable would make the
// plan set it, and a placeholder could break the provider's rules for it.
// An attribute the schema requires keeps its variable, as does one of a
// nested block whose imported blocks don't all lack it. It plans once,
// with the attributes still null as Terraform generated them; if that plan
// doesn't succeed, found is returned as it is. It reports whether it
// removed any attribute.
func withoutUnset(ctx context.Context, tf Terraform, found map[string][]secretAttribute, schemas *tfjson.ProviderSchemas, vars []tfexec.PlanOption) (map[string][]secretAttribute, bool, error) {
	p, _, _, err := showPlanWithSummary(ctx, tf, vars)
	if err != nil || p == nil {
		return found, false, err
	}
	imported := map[string]any{}
	for _, rc := range p.ResourceChanges {
		if rc.Change != nil && rc.Change.Before != nil {
			imported[rc.Address] = rc.Change.Before
		}
	}
	kept := map[string][]secretAttribute{}
	removed := false
	for address, secrets := range found {
		resourceType := strings.SplitN(address, ".", 2)[0]
		for _, s := range secrets {
			before, ok := imported[address]
			attr := schemaAttribute(schemas, resourceType, s.schemaPath)
			if !ok || attr == nil || attr.Required || !allNull(valuesAt(before, s.schemaPath)) {
				kept[address] = append(kept[address], s)
				continue
			}
			s.body.RemoveAttribute(s.name())
			removed = true
		}
	}
	return kept, removed, nil
}

// valuesAt returns the values at path in v, an imported object as a plan
// shows it: an attribute name at each step, through nested blocks, which
// are lists of objects, whatever their index in the configuration. It
// returns nothing where path leads nowhere.
func valuesAt(v any, path []string) []any {
	if len(path) == 0 {
		return []any{v}
	}
	switch v := v.(type) {
	case map[string]any:
		next, ok := v[path[0]]
		if !ok {
			return nil
		}
		if len(path) == 1 {
			return []any{next}
		}
		return valuesAt(next, path[1:])
	case []any:
		var values []any
		for _, item := range v {
			values = append(values, valuesAt(item, path)...)
		}
		return values
	}
	return nil
}

// allNull reports whether values holds at least one value, and every one is
// null.
func allNull(values []any) bool {
	for _, v := range values {
		if v != nil {
			return false
		}
	}
	return len(values) > 0
}

// aboutSecret reports whether d is about one of secrets: its range is on
// the attribute, or its message names the attribute and says a value is
// missing (see missingValue), as in "one of insecure_value,value,value_wo
// must be specified". Such errors are expected while the attributes are
// null and go away once they read from variables; validation after that
// catches any taken for one by mistake. A message that names an attribute
// for another reason, such as "Invalid value", isn't about it: secret
// attributes are often called value or password.
func aboutSecret(d tfjson.Diagnostic, secrets []secretAttribute) bool {
	for _, s := range secrets {
		if d.Range != nil && spans(s.rng, d.Range.Start.Line) {
			return true
		}
	}
	if !missingValue.MatchString(d.Summary + " " + d.Detail) {
		return false
	}
	words := map[string]bool{}
	for _, w := range notInIdentifier.Split(d.Summary+" "+d.Detail, -1) {
		words[w] = true
	}
	for _, s := range secrets {
		if words[s.name()] {
			return true
		}
	}
	return false
}

// missingValue matches what Terraform and the providers say of an argument
// that has no value but needs one.
var missingValue = regexp.MustCompile(`(?i)must be specified|must be set|required|not set|at least one of`)

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
		why := "Terraform doesn't write secret values into the configuration it generates"
		if s.key != "" {
			why = "infraharvest found a secret value here and left it out of the configuration"
		}
		b.SetAttributeValue("description", cty.StringVal(fmt.Sprintf("%s of %s. %s: "+secretStoreAdvice+".", s.Attribute, s.Address, why, s.Variable)))
		ty := s.ty
		if ty == cty.NilType {
			ty = attributeType(schemas, strings.SplitN(s.Address, ".", 2)[0], s.schemaPath)
		}
		if ty != cty.NilType {
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

// publicRule names a sensitive argument whose value isn't secret when
// another argument of the resource doesn't have one of some values.
type publicRule struct {
	attribute string // the sensitive argument
	when      string // the argument that decides
	secretIn  []string
}

// publicSensitive are, by resource type, sensitive arguments whose value
// is often public. An SSM parameter's value is always sensitive in the
// provider, but only a SecureString's is secret: a String or StringList
// parameter's is written into the configuration as it is. Its
// insecure_value would plan an update after import, as the provider reads
// an imported parameter's value into value.
var publicSensitive = map[string]publicRule{
	"aws_ssm_parameter": {attribute: "value", when: "type", secretIn: []string{"SecureString"}},
}

// isPublic reports whether rule says the value of rule.attribute isn't
// secret in a resource whose rule.when argument has value when.
func (rule publicRule) isPublic(when string) bool {
	return when != "" && !slices.Contains(rule.secretIn, when)
}

// publicSecretsToLiterals writes into the configuration the values of the
// secrets publicSensitive says aren't secret, as the plan shows them, and
// returns the other secrets. Values that look like secrets all the same
// (see secretReason) stay in their variables. It plans only if a secret
// may be public.
func publicSecretsToLiterals(ctx context.Context, tf Terraform, dir string, secrets []Secret, opts Options) ([]Secret, error) {
	path := filepath.Join(dir, GeneratedFileName)
	generated, err := loadHCL(path)
	if err != nil {
		return nil, err
	}
	bodies := map[string]*hclsyntax.Body{}
	writes := map[string]*hclwrite.Body{}
	for _, r := range generated.resources() {
		bodies[r.address], writes[r.address] = r.syntax.Body, r.write.Body()
	}
	candidate := func(s Secret) (publicRule, bool) {
		rule, ok := publicSensitive[strings.SplitN(s.Address, ".", 2)[0]]
		if !ok || s.key != "" || s.Attribute != rule.attribute || bodies[s.Address] == nil {
			return rule, false
		}
		return rule, rule.isPublic(stringLiteral(attributeExpr(bodies[s.Address], rule.when)))
	}
	possible := false
	for _, s := range secrets {
		if _, ok := candidate(s); ok {
			possible = true
		}
	}
	if !possible {
		return secrets, nil
	}
	p, _, _, err := showPlanWithSummary(ctx, tf, append(placeholders(secrets), opts.secretValues.options()...))
	if err != nil || p == nil {
		return secrets, err
	}
	imported := map[string]map[string]any{}
	for _, rc := range p.ResourceChanges {
		if rc.Change != nil {
			if before, ok := rc.Change.Before.(map[string]any); ok {
				imported[rc.Address] = before
			}
		}
	}
	var kept []Secret
	changed := false
	for _, s := range secrets {
		rule, ok := candidate(s)
		if !ok {
			kept = append(kept, s)
			continue
		}
		value, isString := imported[s.Address][rule.attribute].(string)
		name := stringLiteral(attributeExpr(bodies[s.Address], "name"))
		if !isString || secretReason(name, value, true) != "" {
			kept = append(kept, s)
			continue
		}
		// Set afresh, without Terraform's "# sensitive" comment.
		writes[s.Address].RemoveAttribute(rule.attribute)
		writes[s.Address].SetAttributeValue(rule.attribute, cty.StringVal(value))
		changed = true
	}
	if !changed {
		return secrets, nil
	}
	if err := generated.save(); err != nil {
		return nil, err
	}
	return kept, writeVariables(dir, kept, nil)
}

// publicValue returns the value of a sensitive argument of rc's imported
// object that publicSensitive says isn't secret, as publicSecretsToLiterals
// writes it.
func publicValue(rc *tfjson.ResourceChange) (string, bool) {
	rule, ok := publicSensitive[rc.Type]
	if !ok || rc.Change == nil {
		return "", false
	}
	before, _ := rc.Change.Before.(map[string]any)
	when, _ := before[rule.when].(string)
	value, isString := before[rule.attribute].(string)
	name, _ := before["name"].(string)
	return value, isString && rule.isPublic(when) && secretReason(name, value, true) == ""
}
