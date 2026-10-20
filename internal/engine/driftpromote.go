// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/zclconf/go-cty/cty"
)

// stagedModulesDir is the directory of drift's staged copy of a root that
// holds copies of the generated modules the root calls, which an
// accepted change may edit (see promoteLiteral) before the root's own
// modules are.
const stagedModulesDir = ".modules"

// generatedModuleName matches the directory name of a generated local
// module (see modularize): its kind and the hash of its shape.
var generatedModuleName = regexp.MustCompile(`^[a-z0-9_]+_[0-9a-f]{8}$`)

// generatedModuleDir reports whether dir is a generated local module's,
// <out>/modules/<kind>_<hash>.
func generatedModuleDir(dir string) bool {
	dir = filepath.ToSlash(filepath.Clean(dir))
	return generatedModuleName.MatchString(path.Base(dir)) && path.Base(path.Dir(dir)) == ModulesDirName
}

// isModuleFile reports whether name, a file of the contents drift edits,
// is a file of a module the root calls, by its path relative to the
// root, which leaves the root (see addModuleFiles).
func isModuleFile(name string) bool {
	return strings.HasPrefix(name, "../")
}

// stageModule copies the generated module in dir into the staged copy in
// staging, under stagedModulesDir, and returns the source that calls the
// copy; "" for a module that calls modules of its own, which stays where
// it is.
func stageModule(staging, dir string) (string, error) {
	contents, err := tfContents(dir)
	if err != nil {
		return "", err
	}
	for name, content := range contents {
		f, err := parseHCL(name, content)
		if err != nil {
			return "", err
		}
		for _, b := range f.syntax.Blocks {
			if b.Type == "module" {
				return "", nil
			}
		}
	}
	copied := filepath.Join(staging, stagedModulesDir, filepath.Base(dir))
	if err := os.MkdirAll(copied, 0o700); err != nil {
		return "", err
	}
	for name, content := range contents {
		if err := os.WriteFile(filepath.Join(copied, name), content, 0o600); err != nil {
			return "", err
		}
	}
	return "./" + stagedModulesDir + "/" + filepath.Base(dir), nil
}

// stagedPath is where the staged copy in staging has name, a file of the
// contents drift edits: a module's file in the module's copy (see
// stageModule).
func stagedPath(staging, name string) string {
	if isModuleFile(name) {
		return filepath.Join(staging, stagedModulesDir, path.Base(path.Dir(name)), path.Base(name))
	}
	return filepath.Join(staging, filepath.FromSlash(name))
}

// addModuleFiles adds to contents, the files of the root in dir, the .tf
// files of the generated modules of modules that call no modules of
// their own, by their paths relative to the root, such as
// ../../../modules/s3_0a1b2c3d/main.tf: an accepted change may edit them
// (see promoteLiteral).
func addModuleFiles(contents map[string][]byte, dir string, modules map[string]*driftModule) error {
	for _, m := range modules {
		if !generatedModuleDir(m.dir) || !isModuleFile(m.dir+"/") || len(m.cfg.calls) > 0 {
			continue
		}
		files, err := tfContents(filepath.Join(dir, filepath.FromSlash(m.dir)))
		if err != nil {
			return err
		}
		for name, content := range files {
			contents[path.Join(m.dir, name)] = content
		}
	}
	return nil
}

// syncModules parses again, from contents, the modules whose files
// contents has (see addModuleFiles), as the edits before have left them.
func (e *driftEditor) syncModules(contents map[string][]byte) {
	parsed := map[string]*driftModule{}
	for key, m := range e.modules {
		if p, ok := parsed[m.dir]; ok {
			e.modules[key] = p
			continue
		}
		files := map[string][]byte{}
		for name, content := range contents {
			if rest, ok := strings.CutPrefix(name, m.dir+"/"); ok && isModuleFile(name) && !strings.Contains(rest, "/") {
				files[rest] = content
			}
		}
		if len(files) == 0 {
			continue
		}
		synced, err := newDriftModule(files)
		if err != nil {
			continue
		}
		synced.dir, synced.calls, synced.inContents = m.dir, m.calls, true
		parsed[m.dir] = synced
		e.modules[key] = synced
	}
}

// promoteLiteral is the source of a change of an attribute that m, a
// generated module the root's call calls, sets in res, the block of the
// resource, to a literal, as t traced it from the steps of the
// attribute's path, where calls share it: other calls of the module, or
// other environments of a root they share. The literal becomes a
// variable of the module whose default is the literal, so that the
// others keep it; and the call sets it to the cloud's value, or, in a
// root environments share, to a variable of the root, of the same
// default, that the environment's var file sets. It returns false for a
// literal it doesn't make a variable: of a module that isn't generated,
// that calls modules, or that no other call or environment shares; of a
// call with count or for_each; or outside the resource's block, as in a
// local of the module.
func (e *driftEditor) promoteLiteral(cfg *driftConfig, c *DriftChange, call driftBlock, callName string, m *driftModule, res driftBlock, t traced, steps []pathStep) (Source, bool) {
	inBlock := t.file == res.file && res.b.Range().Start.Byte <= t.rng.Start.Byte && t.rng.End.Byte <= res.b.Range().End.Byte
	if !m.inContents || !generatedModuleDir(m.dir) || len(m.cfg.calls) > 0 || (m.calls < 2 && e.env == "") || hasInstances(call.b) || !inBlock || t.at == 0 {
		return Source{}, false
	}
	manual := func(reason string) (Source, bool) {
		return Source{File: call.file, Range: call.b.DefRange(), Kind: SourceModule, Reason: fmt.Sprintf("in the module call %s: the module %s sets it to a literal the calls share, which can't be made a variable of it: %s", c.ModuleAddress, m.dir, reason)}, true
	}
	mf := m.cfg.files[t.file]
	if mf == nil || t.rng.End.Byte > len(mf.src) {
		return manual("edit the module by hand")
	}
	literal := string(mf.src[t.rng.Start.Byte:t.rng.End.Byte])
	at := steps[:t.at]
	value := c.value
	if t.at < len(steps) {
		value, _ = valueAt(e.resources[c.Address].after, at)
	}
	if value == nil {
		return manual("the cloud has no value for it: edit it by hand")
	}
	ty := resourceTypeAt(e.schemas, c.Type, at)
	text, err := typedValueText(value, ty)
	if err != nil {
		return manual("the cloud's value can't be written: edit it by hand")
	}
	var words []string
	for _, s := range at {
		if !s.isIndex {
			words = append(words, s.name)
		}
	}
	name := uniqueVariable(Label(strings.Join(words, "_")), m.cfg.variables)
	typeLine := ""
	if ty != cty.NilType && !ty.HasDynamicTypes() {
		typeLine = "type = " + typeexpr.TypeString(ty) + "\n"
	}
	resource := res.b.Labels[0] + "." + res.b.Labels[1]

	// The module: the literal is the variable, whose default it is.
	moduleFile := path.Join(m.dir, t.file)
	edits := []SourceEdit{{File: moduleFile, Start: t.rng.Start.Byte, End: t.rng.End.Byte, Text: []byte("var." + name)}}
	variablesFile := t.file
	if m.cfg.files[VariablesFileName] != nil {
		variablesFile = VariablesFileName
	}
	description := fmt.Sprintf("The %s of %s, the same for every call that doesn't set it.", pathText(at), resource)
	edits = append(edits, appendBlock(m.cfg.files[variablesFile], path.Join(m.dir, variablesFile), variableBlock(name, description, typeLine, literal)))

	w := &driftWalk{e: e, cfg: cfg, c: c, file: call.file, f: call.f, address: "module." + callName}
	src := Source{File: call.file, Range: call.b.DefRange(), Kind: SourceModule, Module: e.viaText(m, t)}
	if e.env == "" {
		// The call sets the cloud's value.
		edit := w.insertArgument(call.b, name, text)
		if edit == nil {
			return manual("the call is on one line: edit it by hand")
		}
		src.Edit, src.Also = edit, edits
		return src, true
	}
	// The call sets a variable of the root, whose default is the literal
	// too, and the environment's var file sets that.
	vars := cfg.files[e.envVars]
	if vars == nil {
		return manual("the root has no " + e.envVars + ": edit it by hand")
	}
	rootName := uniqueVariable(Label(callName+"_"+name), cfg.variables)
	edit := w.insertArgument(call.b, name, []byte("var."+rootName))
	if edit == nil {
		return manual("the call is on one line: edit it by hand")
	}
	edits = append(edits, *edit)
	rootFile := call.file
	if cfg.files[VariablesFileName] != nil {
		rootFile = VariablesFileName
	}
	description = fmt.Sprintf("The %s of module.%s, which an environment's terraform.tfvars sets where it differs from the default.", name, callName)
	edits = append(edits, appendBlock(cfg.files[rootFile], rootFile, variableBlock(rootName, description, typeLine, literal)))
	line := rootName + " = " + string(text) + "\n"
	if n := len(vars.src); n > 0 && vars.src[n-1] != '\n' {
		line = "\n" + line
	}
	src.Edit = &SourceEdit{File: e.envVars, Start: len(vars.src), End: len(vars.src), Text: []byte(line)}
	src.Also = edits
	return src, true
}

// uniqueVariable returns name, or name with _2, _3, ... after it,
// whichever variables doesn't declare.
func uniqueVariable[T any](name string, variables map[string]T) string {
	unique := name
	for n := 2; ; n++ {
		if _, ok := variables[unique]; !ok {
			return unique
		}
		unique = fmt.Sprintf("%s_%d", name, n)
	}
}

// variableBlock writes a variable block of name, with description, a
// type line, if any, and value as its default.
func variableBlock(name, description, typeLine, value string) string {
	return fmt.Sprintf("variable %q {\ndescription = %q\n%sdefault = %s\n}\n", name, description, typeLine, value)
}

// appendBlock is the edit that adds text at the end of f, the file name,
// after a blank line.
func appendBlock(f *hclFile, name, text string) SourceEdit {
	var src []byte
	if f != nil {
		src = f.src
	}
	switch {
	case len(src) == 0:
	case src[len(src)-1] == '\n':
		text = "\n" + text
	default:
		text = "\n\n" + text
	}
	return SourceEdit{File: name, Start: len(src), End: len(src), Text: []byte(text)}
}
