// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// Options configures Convert. The zero value converts with DefaultMapper.
type Options struct {
	// Mapper names the Terraform types, arguments and attributes; nil means
	// DefaultMapper.
	Mapper Mapper
	// StackName, when set, is the default of the stack_name variable that
	// stands in for AWS::StackName, such as the name of the stack the
	// template was deployed as.
	StackName string
	// PreventDestroy adds lifecycle { prevent_destroy = true } to the
	// resources whose DeletionPolicy is Retain or RetainExceptOnCreate.
	//
	// It is off by default because it isn't what Retain means: Retain lets
	// the stack delete the resource from itself and leaves the resource
	// behind, while prevent_destroy makes any plan that would destroy the
	// resource fail, including a replacement and terraform destroy. Without
	// it, the output says in a comment how to keep the resource when it
	// leaves the configuration (a removed block with destroy = false).
	PreventDestroy bool
	// Resolver, when set, names the Terraform side of the template's
	// references instead of the converter: see Resolver.
	Resolver Resolver
}

// Result is what Convert writes: the configuration's files and what it
// couldn't carry over.
type Result struct {
	// Files maps file names (main.tf, variables.tf, locals.tf, outputs.tf,
	// data.tf) to their contents, formatted as terraform fmt would. A file
	// with nothing to hold is left out.
	Files map[string][]byte
	// Diagnostics are in the order of the template's sections: Transform
	// and other template-wide ones first, then Parameters, Mappings,
	// Conditions, Resources and Outputs.
	Diagnostics []Diagnostic
}

// FileNames returns the names of the result's files, sorted.
func (r *Result) FileNames() []string {
	names := make([]string, 0, len(r.Files))
	for name := range r.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasErrors reports whether a diagnostic is an Error: something the output
// leaves out or writes null for.
func (r *Result) HasErrors() bool {
	for _, d := range r.Diagnostics {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// Convert translates a template into Terraform configuration for the AWS
// provider:
//
//   - each parameter becomes a variable, with its constraints as
//     validation blocks; an AWS::SSM::Parameter::Value<> parameter becomes
//     a variable holding the parameter's name and an aws_ssm_parameter data
//     source reading it;
//   - the mappings and conditions become locals;
//   - each resource becomes a resource block, named by the Mapper; one with
//     a Condition gets count = local.<condition> ? 1 : 0, and references to
//     it index [0];
//   - each output becomes an output, null when its condition is false;
//   - intrinsic functions become expressions: Ref and Fn::GetAtt
//     references, Fn::Sub template strings, Fn::Join join(), and so on;
//     pseudo parameters become data sources (AWS::Region, AWS::AccountId,
//     AWS::Partition, AWS::URLSuffix) or stand-in variables
//     (AWS::StackName, AWS::StackId, AWS::NotificationARNs), and
//     Fn::ImportValue a stand-in variable, since Terraform has no stacks or
//     exports.
//
// What it can't convert, such as custom resources, macros and unknown
// functions, is a Diagnostic and a comment in the output, never dropped
// silently. Convert returns an error only for a nil template, for one
// whose values expand to more than 64 MiB of expressions (which takes
// values built to repeat one another, such as nested Fn::Sub variables),
// or if the HCL it writes doesn't parse, which would be a bug in the
// converter. The
// same template and options give the same output, byte for byte.
func Convert(t *Template, opts Options) (*Result, error) {
	if t == nil {
		return nil, errors.New("cfn: no template to convert")
	}
	c := newConverter(t, opts)
	var mainW, varsW, localsW, outputsW, dataW hclWriter
	c.writeTemplateNotes(&mainW)
	c.writeParameters(&varsW)
	c.writeLocals(&localsW)
	c.writeResources(&mainW)
	c.writeOutputs(&outputsW)
	c.writeStandIns(&varsW)
	c.writeData(&dataW)
	if c.tooLarge {
		return nil, fmt.Errorf("cfn: the template converts to more than %d MiB of configuration, which only values that repeat one another many times over make", maxOutput>>20)
	}

	res := &Result{Files: map[string][]byte{}, Diagnostics: c.diags}
	for _, file := range []struct {
		name string
		w    *hclWriter
	}{
		{"data.tf", &dataW},
		{"locals.tf", &localsW},
		{"main.tf", &mainW},
		{"outputs.tf", &outputsW},
		{"variables.tf", &varsW},
	} {
		if strings.TrimSpace(file.w.String()) == "" {
			continue
		}
		out, err := formatFile(file.name, file.w.String())
		if err != nil {
			return nil, err
		}
		res.Files[file.name] = out
	}
	return res, nil
}

// formatFile parses a generated file with hclwrite and formats it.
func formatFile(name, src string) ([]byte, error) {
	f, diags := hclwrite.ParseConfig([]byte(src), name, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("cfn: the generated %s isn't valid HCL, which is a bug in the converter: %s", name, diags.Error())
	}
	out := bytes.Trim(hclwrite.Format(f.Bytes()), "\n")
	return append(out, '\n'), nil
}

// maxOutput bounds the expressions one Convert call writes, in bytes. A
// real template converts to a small fraction of it; a template built to
// expand, such as Fn::Sub variables nested to repeat one another, fails
// instead of exhausting memory.
const maxOutput = 64 << 20

// The pseudo parameters that become stand-in variables, and the names of
// those variables.
const (
	pseudoStackName        = "AWS::StackName"
	pseudoStackID          = "AWS::StackId"
	pseudoNotificationARNs = "AWS::NotificationARNs"
)

var standInVariables = []struct{ pseudo, name string }{
	{pseudoStackName, "stack_name"},
	{pseudoStackID, "stack_id"},
	{pseudoNotificationARNs, "notification_arns"},
}

// The data sources pseudo parameters and Fn::GetAZs read.
const (
	dataRegion    = "aws_region"
	dataCaller    = "aws_caller_identity"
	dataAZs       = "aws_availability_zones"
	dataPartition = "aws_partition"
)

// reservedVariables are names Terraform doesn't allow for a variable.
var reservedVariables = map[string]bool{
	"source": true, "version": true, "providers": true, "count": true, "for_each": true,
	"lifecycle": true, "depends_on": true, "locals": true,
}

type paramInfo struct {
	p    *Parameter
	typ  paramType
	name string // the variable's name
}

type resourceInfo struct {
	r           *Resource
	tfType      string
	tfName      string
	guessedType bool
	// skip says why the resource isn't converted, when it isn't, and
	// skipSeverity how much that matters.
	skip         string
	skipSeverity Severity
}

// address returns the address references to the resource use: indexed
// [0] when the resource has a condition, and so a count.
func (ri *resourceInfo) address() string {
	a := ri.tfType + "." + ri.tfName
	if ri.r.Condition != "" {
		a += "[0]"
	}
	return a
}

type importInfo struct {
	name   string // the stand-in variable's name
	export string // the export's name, as the template writes it
}

// converter holds the state of one Convert call.
type converter struct {
	t      *Template
	opts   Options
	mapper Mapper
	diags  []Diagnostic

	params     map[string]*paramInfo
	resources  map[string]*resourceInfo
	conditions map[string]string // condition name → local name
	mappings   map[string]string // mapping name → local name
	imports    map[string]*importInfo
	importList []*importInfo
	data       map[string]bool
	pseudoUsed map[string]bool
	standIn    map[string]string // pseudo parameter → variable name

	// sensitiveLocals are the condition locals that read a NoEcho
	// parameter, which Terraform marks sensitive.
	sensitiveLocals map[string]bool

	// importNames hands out the names of the variables that stand in for
	// imported values, which share the variables' namespace.
	importNames *nameSet
	outputNames *nameSet

	// notes are comments for the next thing written: the diagnostics of
	// the expression being converted.
	notes []string
	// sensitive is set when an expression reads a sensitive value, so
	// that an output of it can be marked sensitive.
	sensitive bool
	// guessed lists the argument names the mapper guessed in the resource
	// being written.
	guessed     []string
	guessedSeen map[string]bool

	// written counts the bytes of the expressions written so far, and
	// tooLarge is set once that passes maxOutput.
	written  int
	tooLarge bool
	// single is set when the converter converts one value on its own (see
	// ConvertValue): there are no locals, nor stand-in variables, to refer
	// to.
	single bool
}

func newConverter(t *Template, opts Options) *converter {
	mapper := opts.Mapper
	if mapper == nil {
		mapper = DefaultMapper()
	}
	c := &converter{
		t: t, opts: opts, mapper: mapper,
		params: map[string]*paramInfo{}, resources: map[string]*resourceInfo{},
		conditions: map[string]string{}, mappings: map[string]string{},
		imports: map[string]*importInfo{}, data: map[string]bool{},
		pseudoUsed: map[string]bool{}, standIn: map[string]string{},
		sensitiveLocals: map[string]bool{}, outputNames: newNameSet(),
	}
	// Parameters are named first, so that they keep their names and a
	// stand-in variable is the one that gets a suffix if they clash.
	varNames := newNameSet()
	for _, p := range t.Parameters {
		name := ident(p.Name)
		if reservedVariables[name] {
			name += "_value"
		}
		c.params[p.Name] = &paramInfo{p: p, typ: parseParamType(p.Type), name: varNames.claim(name)}
	}
	for _, s := range standInVariables {
		c.standIn[s.pseudo] = varNames.claim(s.name)
	}
	c.importNames = varNames

	localNames := newNameSet()
	for _, cd := range t.Conditions {
		c.conditions[cd.Name] = localNames.claim(ident(cd.Name))
	}
	for _, m := range t.Mappings {
		c.mappings[m.Name] = localNames.claim(ident(m.Name))
	}

	resourceNames := newNameSet()
	for _, r := range t.Resources {
		ri := &resourceInfo{r: r}
		c.resources[r.LogicalID] = ri
		if reason, sev := skipReason(r.Type); reason != "" {
			ri.skip, ri.skipSeverity = reason, sev
			continue
		}
		m, ok := mapper.ResourceType(r.Type)
		switch {
		case !ok:
			ri.skip, ri.skipSeverity = fmt.Sprintf("the mapper has no Terraform resource type for %s, so it isn't converted", r.Type), Error
			continue
		case !identifierPattern.MatchString(m.Name):
			ri.skip, ri.skipSeverity = fmt.Sprintf("the mapper's Terraform type %q for %s isn't a valid type name, so it isn't converted", m.Name, r.Type), Error
			continue
		}
		ri.tfType, ri.guessedType = m.Name, m.Guessed
		ri.tfName = resourceNames.claim(ident(r.LogicalID))
	}
	return c
}

// skipReason returns why the converter leaves out a resource type whatever
// the mapper says, and how much that matters, or "" if it doesn't.
func skipReason(cfnType string) (string, Severity) {
	switch {
	case strings.HasPrefix(cfnType, "Custom::") || cfnType == "AWS::CloudFormation::CustomResource":
		return "a custom resource isn't converted: CloudFormation calls a Lambda function or an SNS topic to create, update and delete it, which Terraform has no equivalent of. Rewrite what the function does as Terraform resources or data sources", Error
	case cfnType == "AWS::CloudFormation::Stack":
		return "a nested stack isn't converted: convert its template on its own and call the result as a module", Error
	case cfnType == "AWS::CloudFormation::WaitCondition" || cfnType == "AWS::CloudFormation::WaitConditionHandle":
		return "a wait condition isn't converted: it holds up the stack until a signal arrives, which Terraform has no equivalent of", Error
	case strings.HasPrefix(cfnType, "AWS::Serverless::"):
		return "a SAM resource isn't converted: the AWS::Serverless transform expands it into other resources, so convert the processed template (aws cloudformation get-template --template-stage Processed)", Error
	case cfnType == "AWS::CDK::Metadata":
		return "the CDK's metadata resource, which reports the CDK version to AWS, is left out", Info
	}
	return "", Info
}

// report records a diagnostic. A warning or an error is also a note: a
// comment in the output where the problem is.
func (c *converter) report(sev Severity, path, msg string) {
	c.diags = append(c.diags, Diagnostic{Severity: sev, Path: path, Message: msg})
	if sev >= Warning {
		c.notes = append(c.notes, note(sev, msg))
	}
}

// remark records a diagnostic that is a note whatever its severity.
func (c *converter) remark(sev Severity, path, msg string) {
	c.diags = append(c.diags, Diagnostic{Severity: sev, Path: path, Message: msg})
	c.notes = append(c.notes, note(sev, msg))
}

// note returns a diagnostic as a comment: a warning or an error starts
// with its severity, so that a search for "error:" finds what is missing.
func note(sev Severity, msg string) string {
	if sev >= Warning {
		return sev.String() + ": " + msg
	}
	return msg
}

// info records an Info diagnostic with no note.
func (c *converter) info(path, msg string) {
	c.diags = append(c.diags, Diagnostic{Severity: Info, Path: path, Message: msg})
}

// keep adds a note with n as the template wrote it, so that what the
// converter leaves out is still in the output.
func (c *converter) keep(n Node) {
	if line := compactJSON(n); len(line) <= 100 {
		c.notes = append(c.notes, "It was: "+line)
		return
	}
	c.notes = append(c.notes, "It was:\n"+toJSON(n))
}

// flush writes the pending notes as comments.
func (c *converter) flush(w *hclWriter) {
	for _, n := range c.notes {
		w.comment(n)
	}
	c.notes = nil
}

// writeAttr writes name = expr after the pending notes. An expression that
// doesn't parse would be a bug in the converter; it is reported and written
// as null, so that one bad expression doesn't lose the whole file.
func (c *converter) writeAttr(w *hclWriter, name, expr, path string) {
	if !exprParses(expr) {
		c.report(Error, path, fmt.Sprintf("the converter wrote an expression for %s that isn't valid HCL, which is a bug; it is null instead: %s", name, expr))
		expr = "null"
	}
	c.flush(w)
	w.attr(name, expr)
}

// writeTemplateNotes reports what applies to the whole template, at the
// top of main.tf.
func (c *converter) writeTemplateNotes(w *hclWriter) {
	for _, tr := range c.t.Transform {
		switch {
		case strings.HasPrefix(tr, "AWS::Serverless"):
			c.report(Error, "Transform", fmt.Sprintf("the template uses the %s transform, which CloudFormation expands into other resources before it deploys them: convert the processed template instead (aws cloudformation get-template --template-stage Processed)", tr))
		case tr == "AWS::LanguageExtensions":
			c.report(Warning, "Transform", "the template uses the AWS::LanguageExtensions transform: Fn::Length, Fn::ToJsonString and Fn::FindInMap defaults are converted, but Fn::ForEach and functions in places only the transform allows aren't")
		default:
			c.report(Error, "Transform", fmt.Sprintf("the template uses the macro %s, which CloudFormation runs on the template before it deploys it: convert the processed template instead (aws cloudformation get-template --template-stage Processed)", tr))
		}
	}
	for _, l := range c.t.Loops {
		c.report(Error, l.Key, "Fn::ForEach isn't converted: it repeats an entry when CloudFormation processes the template. Convert the processed template, or write the entry once with for_each")
		c.keep(l.Value)
	}
	for _, o := range c.t.Other {
		c.report(Warning, o.Key, fmt.Sprintf("the section %s has no Terraform equivalent and isn't converted", o.Key))
		c.keep(o.Value)
	}
	if c.t.Metadata != nil {
		c.info("Metadata", "the template's Metadata isn't carried over: Terraform has no place for it")
	}
	if len(c.notes) > 0 {
		c.flush(w)
		w.blank()
	}
}

// writeLocals writes the mappings and the conditions.
func (c *converter) writeLocals(w *hclWriter) {
	if len(c.t.Mappings) > 0 {
		w.open("locals")
		for _, m := range c.t.Mappings {
			path := "Mappings." + m.Name
			c.writeAttr(w, c.mappings[m.Name], c.expr(m.Value, path, nil), path)
		}
		c.flush(w)
		w.close()
		w.blank()
	}
	if len(c.t.Conditions) > 0 {
		w.open("locals")
		for _, cd := range c.t.Conditions {
			path := "Conditions." + cd.Name
			c.sensitive = false
			expr := c.condition(cd.Expr, path)
			c.sensitiveLocals[c.conditions[cd.Name]] = c.sensitive
			c.writeAttr(w, c.conditions[cd.Name], expr, path)
		}
		c.flush(w)
		w.close()
		w.blank()
	}
}

// writeOutputs writes the outputs.
func (c *converter) writeOutputs(w *hclWriter) {
	for _, o := range c.t.Outputs {
		path := "Outputs." + o.Name
		name := c.outputNames.claim(ident(o.Name))
		if o.Export != nil {
			export := compactJSON(o.Export)
			if s, ok := o.Export.(String); ok {
				export = string(s)
			}
			c.remark(Info, path+".Export", fmt.Sprintf("CloudFormation exports this value as %s. Terraform has no exports: another configuration reads the output from this one's state, with a terraform_remote_state data source", export))
		}
		header := c.notes
		c.notes = nil

		var body hclWriter
		if o.Description != "" {
			body.attr("description", quote(o.Description))
		}
		c.sensitive = false
		value := "null"
		switch {
		case o.Value == nil:
			c.report(Error, path, "the output has no Value; it is null")
		case o.Condition != "":
			cond := c.conditionRef(String(o.Condition), path+".Condition")
			value = fmt.Sprintf("%s ? %s : null", cond, c.operand(o.Value, path+".Value", nil))
		default:
			value = c.topExpr(o.Value, path+".Value", nil)
		}
		c.writeAttr(&body, "value", value, path+".Value")
		if c.sensitive {
			c.info(path, "the output is sensitive: its value comes from a NoEcho parameter")
			body.attr("sensitive", "true")
		}
		c.flush(&body)

		for _, n := range header {
			w.comment(n)
		}
		w.open("output", name)
		w.raw(body.String())
		w.close()
		w.blank()
	}
}

// writeStandIns writes the variables that stand in for pseudo parameters
// and imported values the template uses.
func (c *converter) writeStandIns(w *hclWriter) {
	for _, s := range standInVariables {
		if !c.pseudoUsed[s.pseudo] {
			continue
		}
		w.open("variable", c.standIn[s.pseudo])
		switch s.pseudo {
		case pseudoStackName:
			w.attr("type", "string")
			w.attr("description", quote("Stands in for AWS::StackName: Terraform has no stacks, so set it to the name the template's resources were named after."))
			if c.opts.StackName != "" {
				w.attr("default", quote(c.opts.StackName))
			}
		case pseudoStackID:
			w.attr("type", "string")
			w.attr("description", quote("Stands in for AWS::StackId, the ARN of the CloudFormation stack, which Terraform has no equivalent of."))
		case pseudoNotificationARNs:
			w.attr("type", "list(string)")
			w.attr("description", quote("Stands in for AWS::NotificationARNs, the SNS topics CloudFormation sent the stack's events to."))
			w.attr("default", "[]")
		}
		w.close()
		w.blank()
	}
	for _, iv := range c.importList {
		w.open("variable", iv.name)
		w.attr("type", "string")
		w.attr("description", quote(fmt.Sprintf("Stands in for Fn::ImportValue of the export %s: Terraform has no exports, so set it from the exporting stack's output.", iv.export)))
		w.close()
		w.blank()
	}
}

// writeData writes the data sources the configuration reads.
func (c *converter) writeData(w *hclWriter) {
	for _, d := range []string{dataRegion, dataCaller, dataPartition} {
		if c.data[d] {
			w.raw("data " + quote(d) + " \"current\" {}\n\n")
		}
	}
	if c.data[dataAZs] {
		w.open("data", dataAZs, "available")
		w.attr("state", quote("available"))
		w.close()
		w.blank()
	}
	for _, p := range c.t.Parameters {
		pi := c.params[p.Name]
		if !pi.typ.ssm {
			continue
		}
		w.open("data", "aws_ssm_parameter", pi.name)
		w.attr("name", "var."+pi.name)
		w.close()
		w.blank()
	}
}
