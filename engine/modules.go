// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// ModulesDirName is the directory of the output that holds generated local
// modules, next to the roots.
const ModulesDirName = "modules"

// moduleOutputs are the attributes a generated module outputs for each of
// its resources, where the resource has them: the ones references between
// resources use (see addReferences).
var moduleOutputs = []string{"id", "arn", "name", "bucket"}

// cluster is a resource and its child resources: resources whose type
// extends its type and that refer to it, such as an S3 bucket's versioning.
type cluster struct {
	members []resourceBlock // the parent first, then children in address order
	slots   []string        // each member's name inside the module
}

// slotAddress is a member's address inside the module.
func (c *cluster) slotAddress(i int) []string {
	return []string{resourceTypeOf(c.members[i].address), c.slots[i]}
}

// shapeOf describes what a cluster's module must look like: each member's
// type, arguments and nested blocks.
func (c *cluster) shape() string {
	var b strings.Builder
	for i, m := range c.members {
		fmt.Fprintf(&b, "%s.%s{%s}", resourceTypeOf(m.address), c.slots[i], bodyShape(m.syntax.Body))
	}
	return b.String()
}

func bodyShape(body *hclsyntax.Body) string {
	names := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(strings.Join(names, ","))
	for _, block := range body.Blocks {
		fmt.Fprintf(&b, ";%s{%s}", block.Type, bodyShape(block.Body))
	}
	return b.String()
}

// moduleInput is an argument a generated module takes as a variable.
type moduleInput struct {
	variable string
	ty       cty.Type // cty.DynamicPseudoType if unknown
	what     string   // for the description
	values   []hclwrite.Tokens
	// sensitive is set when a value reads a root variable, which only
	// secrets do (see useVariables): the module's variable is then too.
	sensitive bool
}

// moduleGroup is clusters of one shape, which share a module.
type moduleGroup struct {
	clusters []*cluster
	name     string // the module's directory name
	main     []byte
	inputs   []moduleInput
	outputs  []string // output names, sorted
	// variables and outputsFile are variables.tf and outputs.tf.
	variables   []byte
	outputsFile []byte
}

// modularize moves clusters of resources that occur at least twice with the
// same shape into a generated local module under modulesDir, one call per
// cluster. Values the clusters share stay in the module; values that differ,
// or refer to anything outside the cluster, become variables. It returns
// the module directories it created; it changes nothing when no shape
// repeats, or when moving would break a reference it can't follow.
func modularize(dir, modulesDir string, schemas *tfjson.ProviderSchemas) ([]string, bool, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, false, err
	}
	groups := groupClusters(findClusters(generated))
	if len(groups) == 0 {
		return nil, false, nil
	}
	moved := map[string]string{} // member address -> "call.slot"
	for _, g := range groups {
		ok, err := buildModule(g, schemas)
		if err != nil || !ok {
			return nil, false, err
		}
		for _, c := range g.clusters {
			for i, m := range c.members {
				moved[m.address] = c.members[0].address + "\x00" + c.slots[i]
			}
		}
	}
	if !externalReferencesFollowable(generated, moved, groups) {
		return nil, false, nil
	}

	versions, err := moduleVersionsFile(filepath.Join(dir, VersionsFileName))
	if err != nil {
		return nil, false, err
	}
	var created []string
	for _, g := range groups {
		path := filepath.Join(modulesDir, g.name)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			if err := writeModule(path, g, versions); err != nil {
				return created, false, err
			}
			created = append(created, path)
		} else if err != nil {
			return created, false, err
		}
	}
	if err := rewriteRoot(dir, generated, groups, modulesDir); err != nil {
		return created, false, err
	}
	return created, true, nil
}

// findClusters finds each resource's children: resources whose type is the
// resource's type plus a suffix and that refer to it. A child goes to the
// parent with the longest type, the first by address of those; a resource
// with children isn't a child.
func findClusters(f *hclFile) []*cluster {
	resources := f.resources()
	byAddress := map[string]resourceBlock{}
	for _, r := range resources {
		byAddress[r.address] = r
	}
	children := map[string][]resourceBlock{}
	isChild := map[string]bool{}
	for _, r := range resources {
		parent := ""
		for _, ref := range referencedAddresses(r.syntax.Body) {
			p, ok := byAddress[ref]
			if !ok || p.address == r.address || !strings.HasPrefix(resourceTypeOf(r.address), resourceTypeOf(p.address)+"_") {
				continue
			}
			pt, parentT := resourceTypeOf(p.address), resourceTypeOf(parent)
			if parent == "" || len(pt) > len(parentT) || (len(pt) == len(parentT) && p.address < parent) {
				parent = p.address
			}
		}
		if parent != "" {
			children[parent] = append(children[parent], r)
			isChild[r.address] = true
		}
	}
	var clusters []*cluster
	for _, r := range resources {
		kids := children[r.address]
		if len(kids) == 0 || isChild[r.address] {
			continue
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].address < kids[j].address })
		c := &cluster{members: append([]resourceBlock{r}, kids...), slots: []string{"this"}}
		parentType := resourceTypeOf(r.address)
		used := map[string]bool{"this": true}
		for _, k := range kids {
			base := Label(strings.TrimPrefix(resourceTypeOf(k.address), parentType+"_"))
			slot := base
			for n := 2; used[slot]; n++ {
				slot = fmt.Sprintf("%s_%d", base, n)
			}
			used[slot] = true
			c.slots = append(c.slots, slot)
		}
		clusters = append(clusters, c)
	}
	return clusters
}

// groupClusters returns clusters by shape, for shapes that occur at least
// twice, in a stable order.
func groupClusters(clusters []*cluster) []*moduleGroup {
	byShape := map[string][]*cluster{}
	for _, c := range clusters {
		byShape[c.shape()] = append(byShape[c.shape()], c)
	}
	shapes := make([]string, 0, len(byShape))
	for s, cs := range byShape {
		if len(cs) >= 2 {
			shapes = append(shapes, s)
		}
	}
	sort.Strings(shapes)
	groups := make([]*moduleGroup, 0, len(shapes))
	for _, s := range shapes {
		cs := byShape[s]
		sort.Slice(cs, func(i, j int) bool { return cs[i].members[0].address < cs[j].members[0].address })
		groups = append(groups, &moduleGroup{clusters: cs})
	}
	return groups
}

// referencedAddresses lists the resources the expressions in body refer
// to, sorted, each once.
func referencedAddresses(body *hclsyntax.Body) []string {
	addresses := collectReferencedAddresses(body, nil)
	sort.Strings(addresses)
	return slices.Compact(addresses)
}

func collectReferencedAddresses(body *hclsyntax.Body, addresses []string) []string {
	for _, attr := range body.Attributes {
		for _, t := range attr.Expr.Variables() {
			if a := resourceAddress(t); a != "" {
				addresses = append(addresses, a)
			}
		}
	}
	for _, b := range body.Blocks {
		addresses = collectReferencedAddresses(b.Body, addresses)
	}
	return addresses
}

// resourceAddress returns the resource a traversal refers to, or "".
func resourceAddress(t hcl.Traversal) string {
	switch t.RootName() {
	case "var", "local", "module", "data", "path", "count", "each", "self", "terraform":
		return ""
	}
	if len(t) < 2 {
		return ""
	}
	step, ok := t[1].(hcl.TraverseAttr)
	if !ok {
		return ""
	}
	return t.RootName() + "." + step.Name
}

// buildModule renders the module of a group: the first cluster's resources
// with members renamed to their slots, and a variable for each argument
// whose value differs between the clusters or refers outside its cluster.
// It reports false if a value that differs refers inside its cluster,
// which a variable can't pass.
func buildModule(g *moduleGroup, schemas *tfjson.ProviderSchemas) (bool, error) {
	renamed := make([][]*hclwrite.Block, len(g.clusters))
	for ci, c := range g.clusters {
		for i, m := range c.members {
			block, err := copyBlock(m.write)
			if err != nil {
				return false, err
			}
			block.SetLabels([]string{resourceTypeOf(m.address), c.slots[i]})
			for j, other := range c.members {
				renameIn(block.Body(), strings.Split(other.address, "."), c.slotAddress(j))
			}
			renamed[ci] = append(renamed[ci], block)
		}
	}
	template := renamed[0]
	first := g.clusters[0]
	for i := range first.members {
		bodies := make([]*hclwrite.Body, len(renamed))
		for ci := range renamed {
			bodies[ci] = renamed[ci][i].Body()
		}
		prefix := first.slots[i]
		if prefix == "this" {
			prefix = ""
		}
		if !makeInputs(g, bodies, first, resourceTypeOf(first.members[i].address), nil, prefix, schemas) {
			return false, nil
		}
	}
	f := hclwrite.NewEmptyFile()
	for i, block := range template {
		if i > 0 {
			f.Body().AppendNewline()
		}
		f.Body().AppendBlock(block)
	}
	g.main = hclwrite.Format(f.Bytes())
	for i, m := range first.members {
		for _, attr := range moduleOutputs {
			if schemaAttribute(schemas, resourceTypeOf(m.address), []string{attr}) != nil {
				g.outputs = append(g.outputs, outputName(first.slots[i], attr))
			}
		}
	}
	sort.Strings(g.outputs)
	var err error
	if g.variables, err = moduleVariablesFile(g); err != nil {
		return false, err
	}
	g.outputsFile = moduleOutputsFile(g)
	// Named after the parent's type, without its provider, and the module's
	// content, variables and outputs included: identical clusters in
	// different roots share the module, and a module with other variables
	// or outputs is another module.
	sum := sha256.New()
	for _, content := range [][]byte{g.main, g.variables, g.outputsFile} {
		sum.Write(content)
		sum.Write([]byte{0})
	}
	_, kind, _ := strings.Cut(resourceTypeOf(first.members[0].address), "_")
	g.name = kind + "_" + hex.EncodeToString(sum.Sum(nil))[:8]
	return true, nil
}

func outputName(slot, attr string) string {
	if slot == "this" {
		return attr
	}
	return slot + "_" + attr
}

// makeInputs turns the arguments of one member's bodies (one per cluster;
// the first is the module's) that differ, or refer outside the cluster,
// into variables, and recurses into nested blocks.
func makeInputs(g *moduleGroup, bodies []*hclwrite.Body, first *cluster, resourceType string, schemaPath []string, prefix string, schemas *tfjson.ProviderSchemas) bool {
	internal := map[string]bool{}
	for j := range first.members {
		internal[strings.Join(first.slotAddress(j), ".")] = true
	}
	names := make([]string, 0, len(bodies[0].Attributes()))
	for name := range bodies[0].Attributes() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		values := make([]hclwrite.Tokens, len(bodies))
		same := true
		for ci, body := range bodies {
			values[ci] = body.GetAttribute(name).Expr().BuildTokens(nil)
			if string(values[ci].Bytes()) != string(values[0].Bytes()) {
				same = false
			}
		}
		_, refersOutside := references(values[0], internal)
		if same && !refersOutside {
			continue
		}
		if !same {
			for _, v := range values {
				if in, _ := references(v, internal); in {
					return false
				}
			}
		}
		variable := name
		if prefix != "" {
			variable = prefix + "_" + name
		}
		sensitive := false
		for _, v := range values {
			if readsVariable(v) {
				sensitive = true
			}
		}
		g.inputs = append(g.inputs, moduleInput{
			variable:  variable,
			ty:        attributeTypeOrAny(schemas, resourceType, append(append([]string(nil), schemaPath...), name)),
			what:      fmt.Sprintf("%s of the module's %s", strings.Join(append(append([]string(nil), schemaPath...), name), "."), resourceType),
			values:    values,
			sensitive: sensitive,
		})
		bodies[0].SetAttributeTraversal(name, hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: variable}})
	}
	nested := bodies[0].Blocks()
	for bi, block := range nested {
		inner := make([]*hclwrite.Body, len(bodies))
		for ci, body := range bodies {
			inner[ci] = body.Blocks()[bi].Body()
		}
		segment := block.Type()
		if count := countBlocks(nested, block.Type()); count > 1 {
			segment = fmt.Sprintf("%s_%d", block.Type(), indexOf(nested, block))
		}
		nestedPrefix := segment
		if prefix != "" {
			nestedPrefix = prefix + "_" + segment
		}
		if !makeInputs(g, inner, first, resourceType, append(append([]string(nil), schemaPath...), block.Type()), nestedPrefix, schemas) {
			return false
		}
	}
	return true
}

func countBlocks(blocks []*hclwrite.Block, typ string) int {
	n := 0
	for _, b := range blocks {
		if b.Type() == typ {
			n++
		}
	}
	return n
}

func indexOf(blocks []*hclwrite.Block, block *hclwrite.Block) int {
	i := 0
	for _, b := range blocks {
		if b == block {
			return i
		}
		if b.Type() == block.Type() {
			i++
		}
	}
	return i
}

// readsVariable reports whether tokens refer to a root variable.
func readsVariable(tokens hclwrite.Tokens) bool {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return false
	}
	for _, t := range expr.Variables() {
		if t.RootName() == "var" {
			return true
		}
	}
	return false
}

// references reports whether tokens refer to a resource inside the cluster
// (by its module address), and to anything outside it.
func references(tokens hclwrite.Tokens, internal map[string]bool) (inside, outside bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return false, true
	}
	for _, t := range expr.Variables() {
		if a := resourceAddress(t); a != "" && internal[a] {
			inside = true
		} else {
			outside = true
		}
	}
	return inside, outside
}

func attributeTypeOrAny(schemas *tfjson.ProviderSchemas, resourceType string, path []string) cty.Type {
	if ty := attributeType(schemas, resourceType, path); ty != cty.NilType {
		return ty
	}
	return cty.DynamicPseudoType
}

// copyBlock returns an unattached copy of block, without the comments
// above it (Terraform's, naming the resource's import ID).
func copyBlock(block *hclwrite.Block) (*hclwrite.Block, error) {
	src := block.BuildTokens(nil).Bytes()
	for {
		trimmed := bytes.TrimLeft(src, " \t\r\n")
		if !bytes.HasPrefix(trimmed, []byte("#")) && !bytes.HasPrefix(trimmed, []byte("//")) {
			src = trimmed
			break
		}
		_, rest, found := bytes.Cut(trimmed, []byte("\n"))
		if !found {
			src = nil
			break
		}
		src = rest
	}
	f, diags := hclwrite.ParseConfig(src, "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("copy a block: %w", diags)
	}
	if len(f.Body().Blocks()) != 1 {
		return nil, errors.New("copy a block: not one block")
	}
	copied := f.Body().Blocks()[0]
	f.Body().RemoveBlock(copied)
	return copied, nil
}

// renameIn renames references with prefix search to replacement, in body
// and its nested blocks.
func renameIn(body *hclwrite.Body, search, replacement []string) {
	for _, attr := range body.Attributes() {
		attr.Expr().RenameVariablePrefix(search, replacement)
	}
	for _, b := range body.Blocks() {
		renameIn(b.Body(), search, replacement)
	}
}

// externalReferencesFollowable reports whether every reference from outside
// the moved resources to them names an attribute the module outputs.
func externalReferencesFollowable(f *hclFile, moved map[string]string, groups []*moduleGroup) bool {
	outputs := map[string]bool{}
	for _, g := range groups {
		for _, o := range g.outputs {
			outputs[g.name+"\x00"+o] = true
		}
	}
	moduleOf := map[string]string{}
	for _, g := range groups {
		for _, c := range g.clusters {
			for _, m := range c.members {
				moduleOf[m.address] = g.name
			}
		}
	}
	for _, r := range f.resources() {
		_, isMoved := moved[r.address]
		for _, t := range allTraversals(r.syntax.Body) {
			target := resourceAddress(t)
			call, ok := moved[target]
			if !ok || (isMoved && sameCall(moved[r.address], call)) {
				continue
			}
			if len(t) < 3 {
				return false
			}
			attr, ok := t[2].(hcl.TraverseAttr)
			if !ok {
				return false
			}
			_, slot, _ := strings.Cut(call, "\x00")
			if !outputs[moduleOf[target]+"\x00"+outputName(slot, attr.Name)] {
				return false
			}
		}
	}
	return true
}

func sameCall(a, b string) bool {
	ca, _, _ := strings.Cut(a, "\x00")
	cb, _, _ := strings.Cut(b, "\x00")
	return ca == cb
}

func allTraversals(body *hclsyntax.Body) []hcl.Traversal {
	var ts []hcl.Traversal
	for _, attr := range body.Attributes {
		ts = append(ts, attr.Expr.Variables()...)
	}
	for _, b := range body.Blocks {
		ts = append(ts, allTraversals(b.Body)...)
	}
	return ts
}

// moduleVariablesFile renders a group's variables.tf: a variable per
// input, sensitive where the input is.
func moduleVariablesFile(g *moduleGroup) ([]byte, error) {
	variables := hclwrite.NewEmptyFile()
	for i, in := range g.inputs {
		if i > 0 {
			variables.Body().AppendNewline()
		}
		body := variables.Body().AppendNewBlock("variable", []string{in.variable}).Body()
		body.SetAttributeValue("description", cty.StringVal(strings.ToUpper(in.what[:1])+in.what[1:]+"."))
		tokens, err := typeTokens(in.ty)
		if err != nil {
			return nil, err
		}
		body.SetAttributeRaw("type", tokens)
		if in.sensitive {
			body.SetAttributeValue("sensitive", cty.True)
		}
	}
	return hclwrite.Format(variables.Bytes()), nil
}

// moduleOutputsFile renders a group's outputs.tf.
func moduleOutputsFile(g *moduleGroup) []byte {
	first := g.clusters[0]
	outputs := hclwrite.NewEmptyFile()
	for i, name := range g.outputs {
		if i > 0 {
			outputs.Body().AppendNewline()
		}
		slot, attr := "this", name
		for j, s := range first.slots {
			if s != "this" && strings.HasPrefix(name, s+"_") {
				slot, attr = first.slots[j], strings.TrimPrefix(name, s+"_")
			}
		}
		typ := ""
		for j, s := range first.slots {
			if s == slot {
				typ = resourceTypeOf(first.members[j].address)
			}
		}
		body := outputs.Body().AppendNewBlock("output", []string{name}).Body()
		body.SetAttributeValue("description", cty.StringVal(fmt.Sprintf("The %s of the module's %s.", attr, typ)))
		body.SetAttributeTraversal("value", hcl.Traversal{hcl.TraverseRoot{Name: typ}, hcl.TraverseAttr{Name: slot}, hcl.TraverseAttr{Name: attr}})
	}
	return hclwrite.Format(outputs.Bytes())
}

// writeModule writes a group's module: main.tf, variables.tf, outputs.tf,
// versions.tf and a README.
func writeModule(path string, g *moduleGroup, versions []byte) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	first := g.clusters[0]

	var readme strings.Builder
	fmt.Fprintf(&readme, "# %s\n\nGenerated by infraharvest for %d resources of the same shape: ", g.name, len(g.clusters))
	types := make([]string, 0, len(first.members))
	for _, m := range first.members {
		types = append(types, "`"+resourceTypeOf(m.address)+"`")
	}
	fmt.Fprintf(&readme, "%s.\n\n## Inputs\n\n| Name | Description |\n|---|---|\n", strings.Join(types, ", "))
	for _, in := range g.inputs {
		fmt.Fprintf(&readme, "| `%s` | %s |\n", in.variable, in.what)
	}
	readme.WriteString("\n## Outputs\n\n")
	for _, o := range g.outputs {
		fmt.Fprintf(&readme, "- `%s`\n", o)
	}

	for name, content := range map[string][]byte{
		"main.tf":        g.main,
		"variables.tf":   g.variables,
		"outputs.tf":     g.outputsFile,
		"README.md":      []byte(readme.String()),
		VersionsFileName: versions,
	} {
		if err := os.WriteFile(filepath.Join(path, name), content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// rewriteRoot replaces the clusters' resources in generated.tf with module
// calls, makes references to them use the modules' outputs, and imports
// into the modules.
func rewriteRoot(dir string, generated *hclFile, groups []*moduleGroup, modulesDir string) error {
	source, err := filepath.Rel(dir, modulesDir)
	if err != nil {
		return err
	}
	body := generated.file.Body()
	resources := generated.resources()
	movedBlocks := map[*hclwrite.Block]bool{}
	importTo := map[string][]string{}
	type rename struct{ search, replacement []string }
	var renames []rename
	var calls []*hclwrite.Block
	used := map[string]bool{}
	for _, g := range groups {
		for ci, c := range g.clusters {
			// Named after the parent; unique across the root's module calls.
			base := strings.SplitN(c.members[0].address, ".", 2)[1]
			call := base
			for n := 2; used[call]; n++ {
				call = fmt.Sprintf("%s_%d", base, n)
			}
			used[call] = true
			for i, m := range c.members {
				movedBlocks[m.write] = true
				importTo[m.address] = []string{"module", call, resourceTypeOf(m.address), c.slots[i]}
				typ, name, _ := strings.Cut(m.address, ".")
				for _, attr := range moduleOutputs {
					renames = append(renames, rename{[]string{typ, name, attr}, []string{"module", call, outputName(c.slots[i], attr)}})
				}
			}
			callBlock := hclwrite.NewBlock("module", []string{call})
			callBlock.Body().SetAttributeValue("source", cty.StringVal(filepath.ToSlash(filepath.Join(source, g.name))))
			for _, in := range g.inputs {
				callBlock.Body().SetAttributeRaw(in.variable, in.values[ci])
			}
			calls = append(calls, callBlock)
		}
	}
	for _, r := range resources {
		if movedBlocks[r.write] {
			body.RemoveBlock(r.write)
			continue
		}
		for _, rn := range renames {
			renameIn(r.write.Body(), rn.search, rn.replacement)
		}
	}
	for _, callBlock := range calls {
		for _, rn := range renames {
			renameIn(callBlock.Body(), rn.search, rn.replacement)
		}
		body.AppendNewline()
		body.AppendBlock(callBlock)
	}
	if err := generated.save(); err != nil {
		return err
	}
	imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return err
	}
	for _, imp := range imports.imports() {
		if to, ok := importTo[imp.to]; ok {
			traversal := hcl.Traversal{hcl.TraverseRoot{Name: to[0]}}
			for _, step := range to[1:] {
				traversal = append(traversal, hcl.TraverseAttr{Name: step})
			}
			imp.write.Body().SetAttributeTraversal("to", traversal)
		}
	}
	return imports.save()
}

// liftModules is post-processing's module step: it moves repeated clusters
// into generated modules under modulesDir, installs them, and keeps the
// change only if the plan, with vars, still has the baseline's changes.
func liftModules(ctx context.Context, tf Terraform, dir, modulesDir string, baseline changeSummary, vars []tfexec.PlanOption) (bool, error) {
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return false, fmt.Errorf("terraform providers schema: %w", err)
	}
	backup, err := backupFiles(dir, GeneratedFileName, ImportsFileName)
	if err != nil {
		return false, err
	}
	created, changed, err := modularize(dir, modulesDir, schemas)
	undo := func() error {
		for _, path := range created {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
		return backup.restore()
	}
	if err != nil {
		return false, errors.Join(err, undo())
	}
	if !changed {
		return false, nil
	}
	// The module calls need their modules installed.
	if err := tf.Init(ctx); err != nil {
		return false, errors.Join(fmt.Errorf("terraform init: %w", err), undo())
	}
	diags, summary, err := plan(ctx, tf, vars...)
	if err != nil {
		return false, errors.Join(fmt.Errorf("terraform plan: %w", err), undo())
	}
	if len(diags) == 0 && summary != nil && *summary == baseline {
		return true, nil
	}
	return false, undo()
}

// moduleVersionsFile renders a generated module's versions.tf from its
// root's: the same providers and Terraform versions, as lower bounds only,
// as modules should (~> 6.14 becomes >= 6.14). The root pins the rest.
func moduleVersionsFile(rootVersions string) ([]byte, error) {
	root, err := loadHCL(rootVersions)
	if err != nil {
		return nil, err
	}
	f := hclwrite.NewEmptyFile()
	terraform := f.Body().AppendNewBlock("terraform", nil).Body()
	for _, b := range root.syntax.Blocks {
		if b.Type != "terraform" {
			continue
		}
		if attr, ok := b.Body.Attributes["required_version"]; ok {
			if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String {
				lower, err := lowerBound(v.AsString())
				if err != nil {
					return nil, fmt.Errorf("%s: required_version: %w", rootVersions, err)
				}
				terraform.SetAttributeValue("required_version", cty.StringVal(lower))
			}
		}
		for _, inner := range b.Body.Blocks {
			if inner.Type != "required_providers" {
				continue
			}
			providers := terraform.AppendNewBlock("required_providers", nil).Body()
			names := make([]string, 0, len(inner.Body.Attributes))
			for name := range inner.Body.Attributes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				v, diags := inner.Body.Attributes[name].Expr.Value(nil)
				if diags.HasErrors() || !v.Type().IsObjectType() {
					return nil, fmt.Errorf("%s: required_providers.%s isn't an object", rootVersions, name)
				}
				requirement := map[string]cty.Value{}
				if v.Type().HasAttribute("source") {
					requirement["source"] = v.GetAttr("source")
				}
				if v.Type().HasAttribute("version") {
					lower, err := lowerBound(v.GetAttr("version").AsString())
					if err != nil {
						return nil, fmt.Errorf("%s: required_providers.%s: %w", rootVersions, name, err)
					}
					requirement["version"] = cty.StringVal(lower)
				}
				providers.SetAttributeValue(name, cty.ObjectVal(requirement))
			}
		}
	}
	return hclwrite.Format(f.Bytes()), nil
}

// lowerBound renders a version constraint as its lower bound only, as
// ">= 6.14": the highest version its =, >=, ~> and > parts require. It
// fails when the constraint doesn't parse or has no such part.
func lowerBound(constraint string) (string, error) {
	cs, err := version.NewConstraint(constraint)
	if err != nil {
		return "", err
	}
	var bound *version.Version
	for _, c := range cs {
		text := strings.TrimSpace(c.String())
		op := ""
		for _, candidate := range []string{">=", "<=", "!=", "~>", "=", ">", "<"} {
			if strings.HasPrefix(text, candidate) {
				op = candidate
				break
			}
		}
		switch op {
		case "<", "<=", "!=":
			continue
		}
		v, err := version.NewVersion(strings.TrimSpace(strings.TrimPrefix(text, op)))
		if err != nil {
			return "", err
		}
		if bound == nil || v.GreaterThan(bound) {
			bound = v
		}
	}
	if bound == nil {
		return "", fmt.Errorf("%q has no lower bound", constraint)
	}
	return ">= " + bound.Original(), nil
}
