// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package stackintent carries a CloudFormation stack's template into a root
// generated from the stack's live resources (AWS --cfn-stack): the
// parameters it sets properties from become variables, its outputs become
// outputs, and its conditions are recorded as comments.
//
// The live configuration stays the source of truth, and the root's plan
// must still change nothing, so only edits that can't change a value are
// made, and the verification gate runs after them (see
// engine.Options.Refine):
//
//   - A literal becomes var.<parameter> only where the template sets that
//     very argument to Ref of the parameter, the Mapper knows the argument
//     (not a guess), and the literal equals the parameter's value in the
//     stack, as the same kind of value; the variable's default is that
//     value. The expression's value is the same, so the plan is.
//   - An output is written only when its whole value converts: each
//     resource it refers to is in the root, each attribute is one the
//     provider's schema has, and each parameter has a variable. An output
//     changes no resource.
//   - A variable is declared only when something refers to it, so that no
//     linter reports it unused.
//
// The rest is reported, in the warnings Apply returns and in comments in
// the file it writes, not applied: a parameter used inside Fn::Sub or
// Fn::Join, where the literal is the result of more than the parameter; a
// NoEcho parameter, whose value CloudFormation doesn't give; a property
// whose live value differs from its parameter's, which is drift; an
// output with a condition, or one that refers to what the root doesn't
// have; and the conditions themselves, since what was deployed is what
// the root holds, whatever the condition said.
package stackintent

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/IgnatG/infraharvest/pkg/cfn"
)

// Stack is a CloudFormation stack whose template Apply carries into a root.
type Stack struct {
	Name string
	// ID is the stack's ID, its ARN, which AWS::StackId refers to.
	ID string
	// Template is the template's body, JSON or YAML.
	Template string
	// Parameters are the values the stack's parameters have, by name; nil
	// when they aren't known, as for a nested stack.
	Parameters map[string]string
	// Addresses are the addresses in the root that each resource of the
	// stack was imported at, by logical ID: several when the resource is
	// several Terraform resources, such as a method and its integration.
	Addresses map[string][]string
}

// FileName returns the name of the file Apply writes for a stack.
func FileName(stack string) string {
	return "cloudformation_" + cfn.Name(stack) + ".tf"
}

// Apply carries each stack's template into the root in dir (see the
// package documentation), with mapper naming the Terraform arguments and
// attributes. It edits the root's files where a literal becomes a
// variable, writes a file per stack that has anything to hold (see
// FileName), and returns warnings about what it reported rather than
// applied. It fails only if the root's files can't be read or written.
func Apply(dir string, stacks []Stack, mapper cfn.Mapper) ([]string, error) {
	r, err := loadRoot(dir)
	if err != nil {
		return nil, err
	}
	var warnings []string
	for _, st := range stacks {
		warnings = append(warnings, r.apply(st, mapper)...)
	}
	if err := r.save(); err != nil {
		return nil, err
	}
	return warnings, nil
}

// root is a root's configuration files, as hclwrite edits them.
type root struct {
	dir     string
	files   map[string]*hclwrite.File
	changed map[string]bool
	// resources are the top-level resource blocks, by address.
	resources map[string]*hclwrite.Block
	// names are the root's variable, output and data source names, which
	// what Apply adds must not take.
	variables, outputs map[string]bool
	data               map[string]bool // "<type>.<name>"
	added              map[string]string
}

func loadRoot(dir string) (*root, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	r := &root{dir: dir, files: map[string]*hclwrite.File{}, changed: map[string]bool{}, resources: map[string]*hclwrite.Block{},
		variables: map[string]bool{}, outputs: map[string]bool{}, data: map[string]bool{}, added: map[string]string{}}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		f, diags := hclwrite.ParseConfig(src, path, hcl.InitialPos)
		if diags.HasErrors() {
			return nil, fmt.Errorf("parse %s: %w", path, diags)
		}
		r.files[path] = f
		for _, b := range f.Body().Blocks() {
			labels := b.Labels()
			switch {
			case b.Type() == "resource" && len(labels) == 2:
				r.resources[labels[0]+"."+labels[1]] = b
			case b.Type() == "data" && len(labels) == 2:
				r.data[labels[0]+"."+labels[1]] = true
			case b.Type() == "variable" && len(labels) == 1:
				r.variables[labels[0]] = true
			case b.Type() == "output" && len(labels) == 1:
				r.outputs[labels[0]] = true
			}
		}
	}
	return r, nil
}

// claim returns base, or base with a numeric suffix, as a name taken is.
func claim(taken map[string]bool, base string) string {
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	taken[name] = true
	return name
}

// save writes the files Apply edited and the ones it added.
func (r *root) save() error {
	paths := make([]string, 0, len(r.changed))
	for path := range r.changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := os.WriteFile(path, hclwrite.Format(r.files[path].Bytes()), 0o644); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(r.added))
	for name := range r.added {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		src := append(bytes.TrimRight(hclwrite.Format([]byte(r.added[name])), "\n"), '\n')
		if _, diags := hclsyntax.ParseConfig(src, name, hcl.InitialPos); diags.HasErrors() {
			return fmt.Errorf("the generated %s isn't valid HCL, which is a bug: %w", name, diags)
		}
		if err := os.WriteFile(filepath.Join(r.dir, name), src, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// variable is a parameter that became a variable.
type variable struct {
	param   *cfn.Parameter
	name    string
	typ     string
	literal string    // the default, as HCL
	value   cty.Value // the default's value, to compare literals with
}

// stackRun is Apply's work on one stack.
type stackRun struct {
	r      *root
	st     Stack
	t      *cfn.Template
	mapper cfn.Mapper
	// vars are the variables declared, by parameter; pending those an
	// output being converted asked for, declared only if it is written.
	vars, pending map[string]*variable
	order         []string
	// reported are what is reported rather than applied, for the file's
	// comments and the warnings.
	reported []string
	warnings []string
}

func (r *root) apply(st Stack, mapper cfn.Mapper) []string {
	t, err := cfn.Parse(strings.NewReader(st.Template))
	if err != nil {
		return []string{fmt.Sprintf("CloudFormation stack %s: its template isn't carried into the root: %v", st.Name, err)}
	}
	s := &stackRun{r: r, st: st, t: t, mapper: mapper, vars: map[string]*variable{}}
	s.parameters()
	outputs := s.outputs()
	s.file(outputs)
	return s.warnings
}

// address returns the address in the root of a resource of the template:
// its only one, or, of several, the one of the type the Mapper names.
func (s *stackRun) address(logicalID string) (string, bool) {
	addresses := s.st.Addresses[logicalID]
	if len(addresses) == 1 {
		return addresses[0], s.r.resources[addresses[0]] != nil
	}
	res := s.t.Resource(logicalID)
	if res == nil {
		return "", false
	}
	m, ok := s.mapper.ResourceType(res.Type)
	if !ok {
		return "", false
	}
	for _, a := range addresses {
		if strings.HasPrefix(a, m.Name+".") && s.r.resources[a] != nil {
			return a, true
		}
	}
	return "", false
}

// parameters turns the literals the template sets from a parameter into
// references to its variable, where that is safe (see the package
// documentation), and reports the other uses of parameters.
func (s *stackRun) parameters() {
	for _, res := range s.t.Resources {
		address, ok := s.address(res.LogicalID)
		if !ok {
			continue
		}
		block := s.r.resources[address]
		for _, f := range res.Properties {
			used := referencedParameters(s.t, f.Value)
			if len(used) == 0 {
				continue
			}
			param, direct := directRef(s.t, f.Value)
			if !direct {
				s.report(fmt.Sprintf("%s.%s is computed from the parameters %s, so %s keeps its value", res.LogicalID, f.Key, strings.Join(used, ", "), address))
				continue
			}
			s.substitute(res, address, block, f.Key, param)
		}
	}
}

// substitute writes var.<param> for the argument of address that the
// property key sets, if its literal is the parameter's value.
func (s *stackRun) substitute(res *cfn.Resource, address string, block *hclwrite.Block, key, param string) {
	where := fmt.Sprintf("%s.%s (Ref %s)", res.LogicalID, key, param)
	p, ok := s.mapper.Property(res.Type, []string{key})
	if !ok || p.Guessed || p.Kind != cfn.KindAttribute || p.List {
		s.report(where + ": no argument of " + address + " is known to be the property, so it keeps its value")
		return
	}
	attr := block.Body().GetAttribute(p.Name)
	if attr == nil {
		// The provider's default, which the root leaves out.
		return
	}
	value, literal := literalValue(attr)
	if !literal {
		return
	}
	s.pending = map[string]*variable{}
	defer s.discard()
	v, why := s.variable(param)
	if v == nil {
		s.report(where + ": " + why)
		return
	}
	if !sameValue(value, v.value) {
		s.warn(fmt.Sprintf("%s.%s differs from the value of the parameter %s, which sets it in the template: the stack has drifted, and %s keeps the live value", address, p.Name, param, address))
		return
	}
	s.commit()
	block.Body().SetAttributeTraversal(p.Name, hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: v.name}})
	for path, f := range s.r.files {
		if f.Body() == nil {
			continue
		}
		for _, b := range f.Body().Blocks() {
			if b == block {
				s.r.changed[path] = true
			}
		}
	}
}

// variable returns the variable of a parameter, or why it has none. A
// variable not declared yet is pending, its name reserved, until commit
// declares it or discard lets the name go: only what is written refers to
// a variable, and only a variable something refers to is declared.
func (s *stackRun) variable(name string) (*variable, string) {
	if v, ok := s.vars[name]; ok {
		return v, ""
	}
	if v, ok := s.pending[name]; ok {
		return v, ""
	}
	p := s.t.Parameter(name)
	switch {
	case p == nil:
		return nil, "the template has no such parameter"
	case p.NoEcho:
		return nil, "the parameter is NoEcho, so CloudFormation doesn't give its value"
	}
	live, ok := s.st.Parameters[name]
	if !ok {
		return nil, "the stack's value of the parameter isn't known"
	}
	typ, literal, value, ok := parameterValue(p.Type, live)
	if !ok {
		return nil, fmt.Sprintf("the stack's value of the parameter, %q, isn't a %s", live, p.Type)
	}
	v := &variable{param: p, name: claim(s.r.variables, cfn.VariableName(name)), typ: typ, literal: literal, value: value}
	s.pending[name] = v
	return v, ""
}

// commit declares the pending variables.
func (s *stackRun) commit() {
	for _, name := range sortedKeys(s.pending) {
		s.vars[name] = s.pending[name]
		s.order = append(s.order, name)
	}
	s.pending = map[string]*variable{}
}

// discard lets the names of the pending variables go.
func (s *stackRun) discard() {
	for _, v := range s.pending {
		delete(s.r.variables, v.name)
	}
	s.pending = map[string]*variable{}
}

func (s *stackRun) report(msg string) {
	s.reported = append(s.reported, msg)
}

func (s *stackRun) warn(msg string) {
	s.reported = append(s.reported, msg)
	s.warnings = append(s.warnings, "CloudFormation stack "+s.st.Name+": "+msg)
}

// output is an output of the template, converted.
type output struct {
	o    *cfn.Output
	name string
	expr string
	data []string
	// sensitive says the value reads an attribute the provider marks
	// sensitive, which only a sensitive output may hold.
	sensitive bool
}

// resolver resolves the references of an output's value (see
// cfn.Resolver).
type resolver struct{ s *stackRun }

func (r resolver) Resource(logicalID string) (string, bool) {
	return r.s.address(logicalID)
}

func (r resolver) Parameter(name string) (string, bool) {
	switch name {
	case "AWS::StackName":
		return quoted(r.s.st.Name), true
	case "AWS::StackId":
		if r.s.st.ID != "" {
			return quoted(r.s.st.ID), true
		}
		return "", false
	}
	if strings.HasPrefix(name, "AWS::") {
		return "", false
	}
	v, _ := r.s.variable(name)
	if v == nil {
		return "", false
	}
	return "var." + v.name, true
}

// outputs converts the template's outputs that convert whole (see the
// package documentation), and reports the others.
func (s *stackRun) outputs() []output {
	var out []output
	var skipped []string
	for _, o := range s.t.Outputs {
		if o.Condition != "" {
			skipped = append(skipped, o.Name+" (it has the condition "+o.Condition+")")
			continue
		}
		if o.Value == nil {
			continue
		}
		s.pending = map[string]*variable{}
		v, err := cfn.ConvertValue(s.t, o.Value, "Outputs."+o.Name+".Value", cfn.Options{Mapper: s.mapper, Resolver: resolver{s}})
		problem := ""
		switch {
		case err != nil:
			problem = err.Error()
		case !refersToResource(s.t, o.Value):
			// One with nothing of the root's would be the same in every
			// root of the stack.
			problem = "it refers to no resource"
		default:
			for _, d := range v.Diagnostics {
				if d.Severity >= cfn.Warning {
					problem = d.Message
					break
				}
			}
		}
		if problem != "" {
			s.discard()
			skipped = append(skipped, o.Name+" ("+problem+")")
			continue
		}
		s.commit()
		out = append(out, output{o: o, name: claim(s.r.outputs, cfn.Name(o.Name)), expr: v.Expr, data: v.Data, sensitive: v.Sensitive})
	}
	if len(skipped) > 0 {
		s.warn("outputs not written in this root: " + strings.Join(skipped, "; "))
	}
	return out
}

// file writes the stack's file: its variables, the data sources its
// outputs read, its outputs, and comments on what is reported only.
func (s *stackRun) file(outputs []output) {
	var b strings.Builder
	for _, name := range s.order {
		v := s.vars[name]
		desc := strings.TrimSpace(v.param.Description)
		if desc == "" {
			desc = fmt.Sprintf("The %s parameter of the CloudFormation stack %s.", name, s.st.Name)
		}
		fmt.Fprintf(&b, "variable %s {\n  type        = %s\n  description = %s\n  default     = %s\n", quoted(v.name), v.typ, quoted(desc), v.literal)
		if allowed, ok := allowedValues(v); ok {
			fmt.Fprintf(&b, "\n  validation {\n    condition     = contains(%s, var.%s)\n    error_message = %s\n  }\n", allowed, v.name, quoted(fmt.Sprintf("The value must be one the template allows for %s.", name)))
		}
		b.WriteString("}\n\n")
	}
	for _, o := range outputs {
		for _, d := range o.data {
			key := dataKey(d)
			if s.r.data[key] {
				continue
			}
			s.r.data[key] = true
			b.WriteString(d + "\n")
		}
	}
	for _, o := range outputs {
		desc := strings.TrimSpace(o.o.Description)
		if desc == "" {
			desc = fmt.Sprintf("The %s output of the CloudFormation stack %s.", o.o.Name, s.st.Name)
		}
		if o.o.Export != nil {
			export := cfnText(o.o.Export)
			fmt.Fprintf(&b, "# CloudFormation exported this output as %s: Terraform has no exports, so another configuration reads it from this root's state.\n", export)
		}
		fmt.Fprintf(&b, "output %s {\n  description = %s\n  value       = %s", quoted(o.name), quoted(desc), o.expr)
		if !strings.HasSuffix(o.expr, "\n") {
			b.WriteString("\n")
		}
		if o.sensitive {
			b.WriteString("  sensitive   = true\n")
		}
		b.WriteString("}\n\n")
	}
	if b.Len() == 0 && len(s.reported) == 0 && len(s.t.Conditions) == 0 {
		return
	}
	var head strings.Builder
	fmt.Fprintf(&head, "# From the template of the CloudFormation stack %s: the parameters it\n# sets arguments from, as variables whose defaults are the stack's values,\n# and its outputs. The resources keep their live configuration.\n", s.st.Name)
	if len(s.t.Conditions) > 0 {
		head.WriteString("#\n# Its conditions, which decided what the stack has; the root has what was\n# deployed, so they decide nothing here:\n")
		for _, c := range s.t.Conditions {
			fmt.Fprintf(&head, "#   %s: %s\n", c.Name, cfnText(c.Expr))
		}
	}
	if len(s.reported) > 0 {
		head.WriteString("#\n# Not carried over:\n")
		for _, r := range s.reported {
			fmt.Fprintf(&head, "#   - %s\n", r)
		}
	}
	head.WriteString("\n")
	s.r.added[FileName(s.st.Name)] += head.String() + b.String()
}

// dataKey returns "<type>.<name>" of a data source block's text.
func dataKey(block string) string {
	f, diags := hclsyntax.ParseConfig([]byte(block), "data.tf", hcl.InitialPos)
	if diags.HasErrors() {
		return block
	}
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if len(b.Labels) == 2 {
			return b.Labels[0] + "." + b.Labels[1]
		}
	}
	return block
}

// cfnText returns a template value as one line, for a comment.
func cfnText(n cfn.Node) string {
	if s, ok := n.(cfn.String); ok {
		return string(s)
	}
	var b bytes.Buffer
	if err := writeJSON(&b, n); err != nil {
		return "(a value)"
	}
	return b.String()
}

func sortedKeys(m map[string]*variable) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// quoted returns s as a quoted HCL string.
func quoted(s string) string {
	return string(hclwrite.TokensForValue(cty.StringVal(s)).Bytes())
}

// literalValue returns an attribute's value when its expression is a
// literal, one that refers to nothing.
func literalValue(attr *hclwrite.Attribute) (cty.Value, bool) {
	src := attr.Expr().BuildTokens(nil).Bytes()
	expr, diags := hclsyntax.ParseExpression(src, "expr", hcl.InitialPos)
	if diags.HasErrors() || len(expr.Variables()) > 0 {
		return cty.NilVal, false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() {
		return cty.NilVal, false
	}
	return v, true
}

// sameValue reports whether a literal's value is want, as the same kind
// of value: a string for a string, a number for a number, a list of them
// for a list.
func sameValue(got, want cty.Value) bool {
	switch {
	case want.Type() == cty.String:
		if got.Type() == cty.Number || got.Type() == cty.Bool {
			// A string parameter, such as "30" or "true", that sets a number
			// or a bool: Terraform converts the variable's string to the
			// argument's type, so the value is the same when the literal
			// converts back to the string.
			converted, err := convert.Convert(got, cty.String)
			return err == nil && converted.AsString() == want.AsString()
		}
		return got.Type() == cty.String && got.AsString() == want.AsString()
	case want.Type() == cty.Number:
		return got.Type() == cty.Number && got.AsBigFloat().Cmp(want.AsBigFloat()) == 0
	case want.Type().IsListType():
		if !got.Type().IsTupleType() && !got.Type().IsListType() || got.LengthInt() != want.LengthInt() {
			return false
		}
		gotElems, wantElems := got.AsValueSlice(), want.AsValueSlice()
		for i := range wantElems {
			if !sameValue(gotElems[i], wantElems[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// parameterValue returns the variable type of a parameter type, and the
// stack's value of the parameter as an HCL literal and as a value, or
// false when the value isn't one of the type. A list's value is its
// comma-separated items, each trimmed of spaces.
func parameterValue(paramType, live string) (string, string, cty.Value, bool) {
	inner := paramType
	if strings.HasPrefix(inner, "AWS::SSM::Parameter::Value<") && strings.HasSuffix(inner, ">") {
		inner = strings.TrimSuffix(strings.TrimPrefix(inner, "AWS::SSM::Parameter::Value<"), ">")
	}
	switch {
	case inner == "Number":
		lit, n, ok := numberValue(live)
		if !ok {
			return "", "", cty.NilVal, false
		}
		return "number", lit, cty.NumberVal(n), true
	case inner == "List<Number>":
		var lits []string
		var values []cty.Value
		for _, item := range items(live) {
			lit, n, ok := numberValue(item)
			if !ok {
				return "", "", cty.NilVal, false
			}
			lits, values = append(lits, lit), append(values, cty.NumberVal(n))
		}
		return "list(number)", "[" + strings.Join(lits, ", ") + "]", listOf(cty.Number, values), true
	case inner == "CommaDelimitedList" || strings.HasPrefix(inner, "List<"):
		var lits []string
		var values []cty.Value
		for _, item := range items(live) {
			lits, values = append(lits, quoted(item)), append(values, cty.StringVal(item))
		}
		return "list(string)", "[" + strings.Join(lits, ", ") + "]", listOf(cty.String, values), true
	}
	return "string", quoted(live), cty.StringVal(live), true
}

func listOf(t cty.Type, values []cty.Value) cty.Value {
	if len(values) == 0 {
		return cty.ListValEmpty(t)
	}
	return cty.ListVal(values)
}

// items splits a list parameter's value.
func items(live string) []string {
	if strings.TrimSpace(live) == "" {
		return nil
	}
	parts := strings.Split(live, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// numberValue returns a number as an HCL literal (see cfn.NumberLiteral)
// and as a value.
func numberValue(s string) (string, *big.Float, bool) {
	lit, ok := cfn.NumberLiteral(s)
	if !ok {
		return "", nil, false
	}
	n, ok := number(lit)
	return lit, n, ok
}

// number parses a number as HCL writes one.
func number(s string) (*big.Float, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "xXpP_") {
		return nil, false
	}
	n, ok := new(big.Float).SetString(s)
	return n, ok
}

// allowedValues returns a scalar variable's AllowedValues as an HCL list,
// when its default is one of them: the validation then can't fail on the
// default.
func allowedValues(v *variable) (string, bool) {
	if len(v.param.AllowedValues) == 0 || (v.typ != "string" && v.typ != "number") {
		return "", false
	}
	var lits []string
	found := false
	for _, a := range v.param.AllowedValues {
		var text string
		switch t := a.(type) {
		case cfn.String:
			text = string(t)
		case cfn.Number:
			text = string(t)
		default:
			return "", false
		}
		if v.typ == "number" {
			n, ok := number(text)
			if !ok {
				return "", false
			}
			found = found || n.Cmp(v.value.AsBigFloat()) == 0
			lits = append(lits, text)
			continue
		}
		found = found || text == v.value.AsString()
		lits = append(lits, quoted(text))
	}
	return "[" + strings.Join(lits, ", ") + "]", found
}
