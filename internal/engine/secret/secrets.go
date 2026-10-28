// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package secret keeps secret values out of the configuration infraharvest
// generates: it finds them, among the values Terraform marks sensitive and
// those it writes in the open, moves them into sensitive variables, hides
// them from the output of infraharvest's own plans, and tells whether a
// file holds one. It never writes a value into the output.
package secret

import (
	"context"
	"fmt"
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

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// Variable is a variable the generated configuration reads a secret value
// from. Terraform doesn't write the values the provider marks sensitive
// into the configuration it generates, and infraharvest moves the secrets
// it finds among the others out of it (see LiteralsToVariables), so
// the user sets the variable before planning.
type Variable struct {
	Variable  string // variable name
	Address   string // address of the resource that uses it
	Attribute string // attribute it sets, e.g. value, block[1].password or environment.variables.API_SECRET
	// Type is the variable's type, from the provider schema where it
	// describes the attribute; cty.NilType if unknown.
	Type cty.Type `json:"-"`

	schemaPath []string // block types and attribute name, e.g. [block password]
	// key is the name a secret infraharvest found goes by, such as the
	// environment variable API_SECRET, and value its value, which only
	// infraharvest's own plans see (see Values); both are "" for
	// values the provider marks sensitive, which Terraform doesn't show.
	key, value string
}

// secretStoreAdvice ends the description of every secret variable.
const secretStoreAdvice = "set this variable from your secret store, such as AWS Secrets Manager, Azure Key Vault or Google Secret Manager, or with TF_VAR_%s, and keep the value out of version control"

// Sensitive is an attribute Terraform generated as
// "null # sensitive": it has a value Terraform won't show.
type Sensitive struct {
	address    string
	path       string   // e.g. value or block[1].password
	schemaPath []string // block types and attribute name, e.g. [block password]
	body       *hclwrite.Body
	rng        hcl.Range
}

func (s Sensitive) name() string {
	return s.schemaPath[len(s.schemaPath)-1]
}

// FindSensitive lists the secret attributes of every resource in f, by
// resource address.
func FindSensitive(f *hclfile.File) map[string][]Sensitive {
	secrets := map[string][]Sensitive{}
	for _, r := range f.Resources() {
		if found := secretsIn(r.Address, r.Syntax.Body, r.Write.Body(), "", nil); len(found) > 0 {
			secrets[r.Address] = found
		}
	}
	return secrets
}

func secretsIn(address string, syntax *hclsyntax.Body, body *hclwrite.Body, prefix string, schemaPrefix []string) []Sensitive {
	var found []Sensitive
	names := make([]string, 0, len(syntax.Attributes))
	for name := range syntax.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if attr := body.GetAttribute(name); attr != nil && isSecretNull(attr) {
			found = append(found, Sensitive{
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

// WithoutUnset leaves out of found, and removes from their resources, the
// secret attributes the imported objects hold no value for, such as a
// database's master password, which AWS never returns, or hold the empty
// string, as the google provider does for an encryption key never set:
// unset, the argument plans as what was imported, where a variable would
// make the plan set it, and a placeholder could break the provider's rules
// for it.
// An attribute the schema requires keeps its variable, as does one of a
// nested block whose imported blocks don't all lack it. It plans once,
// with the attributes still null as Terraform generated them; if that plan
// doesn't succeed, found is returned as it is. It reports whether it
// removed any attribute.
func WithoutUnset(ctx context.Context, tf tfrun.Terraform, found map[string][]Sensitive, schemas *tfjson.ProviderSchemas, vars []tfexec.PlanOption) (map[string][]Sensitive, bool, error) {
	p, _, _, err := tfrun.ShowPlan(ctx, tf, vars)
	if err != nil || p == nil {
		return found, false, err
	}
	imported := map[string]any{}
	for _, rc := range p.ResourceChanges {
		if rc.Change != nil && rc.Change.Before != nil {
			imported[rc.Address] = rc.Change.Before
		}
	}
	kept := map[string][]Sensitive{}
	removed := false
	for address, secrets := range found {
		resourceType := strings.SplitN(address, ".", 2)[0]
		for _, s := range secrets {
			before, ok := imported[address]
			attr := tfrun.SchemaAttribute(schemas, resourceType, s.schemaPath)
			if !ok || attr == nil || attr.Required || !allUnset(valuesAt(before, s.schemaPath)) {
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

// allUnset reports whether values holds at least one value, and every one is
// null or the empty string, which providers built on the plugin SDK, such
// as google, record for an optional string never set, such as a disk's
// encryption key.
func allUnset(values []any) bool {
	for _, v := range values {
		if v != nil && v != "" {
			return false
		}
	}
	return len(values) > 0
}

// About reports whether d is about one of secrets: its range is on
// the attribute, or its message names the attribute and says a value is
// missing (see missingValue), as in "one of insecure_value,value,value_wo
// must be specified". Such errors are expected while the attributes are
// null and go away once they read from variables; validation after that
// catches any taken for one by mistake. A message that names an attribute
// for another reason, such as "Invalid value", isn't about it: secret
// attributes are often called value or password.
func About(d tfjson.Diagnostic, secrets []Sensitive) bool {
	for _, s := range secrets {
		if d.Range != nil && hclfile.Spans(s.rng, d.Range.Start.Line) {
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

// ToVariables makes every secret attribute read from a new sensitive
// variable, named other than the variables in taken, and returns the
// variables, by resource in file order and by attribute as secretsIn
// lists them (by name, then nested blocks in order), typed from schemas
// where they describe the attribute. It edits f's tree.
func ToVariables(f *hclfile.File, secrets map[string][]Sensitive, taken map[string]bool, schemas *tfjson.ProviderSchemas) []Variable {
	var vars []Variable
	used := map[string]bool{}
	for name := range taken {
		if v, ok := strings.CutPrefix(name, "var."); ok {
			used[v] = true
		}
	}
	for _, r := range f.Resources() {
		for _, s := range secrets[r.Address] {
			// Shells can only set TF_VAR_<name> for names without dashes.
			base := strings.Trim(notInVariableName.ReplaceAllString(s.address+"_"+s.path, "_"), "_")
			name := base
			for i := 2; used[name]; i++ {
				name = fmt.Sprintf("%s_%d", base, i)
			}
			used[name] = true
			s.body.SetAttributeTraversal(s.name(), hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: name}})
			ty := tfrun.AttributeType(schemas, strings.SplitN(s.address, ".", 2)[0], s.schemaPath)
			vars = append(vars, Variable{Variable: name, Address: s.address, Attribute: s.path, Type: ty, schemaPath: s.schemaPath})
		}
	}
	return vars
}

// VariablesFile renders a sensitive variable without a default for each
// secret, typed from the provider schemas where they describe it.
func VariablesFile(secrets []Variable, schemas *tfjson.ProviderSchemas) ([]byte, error) {
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
		ty := s.Type
		if ty == cty.NilType {
			ty = tfrun.AttributeType(schemas, strings.SplitN(s.Address, ".", 2)[0], s.schemaPath)
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

// WithoutWriteOnly leaves out write-only attributes (value_wo, for
// example). Terraform also generates them as "null # sensitive", but they
// are never stored, so null, unset, is what an import should produce.
func WithoutWriteOnly(secrets map[string][]Sensitive, schemas *tfjson.ProviderSchemas) map[string][]Sensitive {
	kept := map[string][]Sensitive{}
	for addr, attrs := range secrets {
		resourceType := strings.SplitN(addr, ".", 2)[0]
		for _, s := range attrs {
			if attr := tfrun.SchemaAttribute(schemas, resourceType, s.schemaPath); attr == nil || !attr.WriteOnly {
				kept[addr] = append(kept[addr], s)
			}
		}
	}
	return kept
}

// typeTokens renders ty as a type constraint expression.
func typeTokens(ty cty.Type) (hclwrite.Tokens, error) {
	f, diags := hclwrite.ParseConfig([]byte("type = "+typeexpr.TypeString(ty)+"\n"), "type", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	return f.Body().GetAttribute("type").Expr().BuildTokens(nil), nil
}

// publicSensitive returns the sensitive argument of a resource type whose
// value is often public, as the catalogue has it (see
// catalogue.SecretWhen), such as an SSM parameter's value, which is a
// secret only in a SecureString parameter.
func publicSensitive(typ string) (catalogue.SecretWhen, bool) {
	return catalogue.ForType(typ).SecretWhen(typ)
}

// isPublic reports whether rule says the value of its argument isn't
// secret in a resource whose rule.When argument has value when.
func isPublic(rule catalogue.SecretWhen, when string) bool {
	return when != "" && !slices.Contains(rule.In, when)
}

// PublicToLiterals writes into the generated configuration, the file at
// path, the values of the secrets publicSensitive says aren't secret, as
// the plan shows them, and returns the other secrets, and whether it
// wrote any, which leaves their variables to declare no more. Values that look like secrets all the same
// (see Reason) stay in their variables. It plans only if a secret
// may be public, with the values of known, if set, for the secrets Generate
// found (see Values).
func PublicToLiterals(ctx context.Context, tf tfrun.Terraform, path string, secrets []Variable, known *Values) ([]Variable, bool, error) {
	generated, err := hclfile.Load(path)
	if err != nil {
		return nil, false, err
	}
	bodies := map[string]*hclsyntax.Body{}
	writes := map[string]*hclwrite.Body{}
	for _, r := range generated.Resources() {
		bodies[r.Address], writes[r.Address] = r.Syntax.Body, r.Write.Body()
	}
	candidate := func(s Variable) (catalogue.SecretWhen, bool) {
		rule, ok := publicSensitive(strings.SplitN(s.Address, ".", 2)[0])
		if !ok || s.key != "" || s.Attribute != rule.Argument || bodies[s.Address] == nil {
			return rule, false
		}
		return rule, isPublic(rule, hclfile.StringLiteral(hclfile.AttributeExpr(bodies[s.Address], rule.When)))
	}
	possible := false
	for _, s := range secrets {
		if _, ok := candidate(s); ok {
			possible = true
		}
	}
	if !possible {
		return secrets, false, nil
	}
	p, _, _, err := tfrun.ShowPlan(ctx, tf, append(Placeholders(secrets), known.Options()...))
	if err != nil || p == nil {
		return secrets, false, err
	}
	imported := map[string]map[string]any{}
	for _, rc := range p.ResourceChanges {
		if rc.Change != nil {
			if before, ok := rc.Change.Before.(map[string]any); ok {
				imported[rc.Address] = before
			}
		}
	}
	var kept []Variable
	changed := false
	for _, s := range secrets {
		rule, ok := candidate(s)
		if !ok {
			kept = append(kept, s)
			continue
		}
		value, isString := imported[s.Address][rule.Argument].(string)
		name := hclfile.StringLiteral(hclfile.AttributeExpr(bodies[s.Address], "name"))
		if !isString || Reason(name, value, true) != "" {
			kept = append(kept, s)
			continue
		}
		// Set afresh, without Terraform's "# sensitive" comment.
		writes[s.Address].RemoveAttribute(rule.Argument)
		writes[s.Address].SetAttributeValue(rule.Argument, cty.StringVal(value))
		changed = true
	}
	if !changed {
		return secrets, false, nil
	}
	if err := generated.Save(); err != nil {
		return nil, false, err
	}
	return kept, true, nil
}

// publicValue returns the value of a sensitive argument of rc's imported
// object that publicSensitive says isn't secret, as PublicToLiterals
// writes it.
func publicValue(rc *tfjson.ResourceChange) (string, bool) {
	rule, ok := publicSensitive(rc.Type)
	if !ok || rc.Change == nil {
		return "", false
	}
	before, _ := rc.Change.Before.(map[string]any)
	when, _ := before[rule.When].(string)
	value, isString := before[rule.Argument].(string)
	name, _ := before["name"].(string)
	return value, isString && isPublic(rule, when) && Reason(name, value, true) == ""
}

// PlaceholderValue stands in for secret values in the plans post-processing
// runs. Those plans are only compared with each other, never applied. It
// meets the password rules providers check before planning (Redshift's,
// RDS's, Azure's): upper and lower case letters, a digit and a symbol, 8
// to 41 characters, and none of / @ " ' \ or a space.
const PlaceholderValue = "Infraharvest-Placeholder-0"

// Placeholders gives each secret variable without a known value (see
// Values) a value of its type.
func Placeholders(secrets []Variable) []tfexec.PlanOption {
	vars := make([]tfexec.PlanOption, 0, len(secrets))
	for _, s := range secrets {
		if s.value != "" {
			continue
		}
		value := PlaceholderValue
		switch {
		case s.Type.Equals(cty.Number):
			value = "0"
		case s.Type.Equals(cty.Bool):
			value = "false"
		case s.Type.IsListType(), s.Type.IsSetType(), s.Type.IsTupleType():
			value = "[]"
		case s.Type.IsMapType(), s.Type.IsObjectType():
			value = "{}"
		}
		vars = append(vars, tfexec.Var(s.Variable+"="+value))
	}
	return vars
}
