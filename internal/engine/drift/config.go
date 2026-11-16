// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// driftConfig is a root's configuration files, parsed, as resolve reads
// them.
type driftConfig struct {
	files     map[string]*hclfile.File // by name, relative to the root
	blocks    map[string]driftBlock
	locals    map[string]driftLocal
	uses      map[string]int // how often each local is referred to
	varUses   map[string]int // and each variable, outside variables and outputs
	variables map[string]*hclsyntax.Block
	calls     map[string]driftBlock // module calls, by name

	// parsed are the configurations of each file alone, by name, which
	// reparse keeps for the files an edit leaves as they were.
	parsed map[string]*driftConfig
}

type driftBlock struct {
	file string
	f    *hclfile.File
	b    *hclsyntax.Block
}

type driftLocal struct {
	file string
	f    *hclfile.File
	attr *hclsyntax.Attribute
}

// namedFile is a file of a driftConfig, with its name.
type namedFile struct {
	name string
	f    *hclfile.File
}

// tfContents returns the contents of the .tf files of dir, by name,
// without override files.
func tfContents(dir string) (map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	contents := map[string][]byte{}
	for _, path := range paths {
		name := filepath.Base(path)
		if name == "override.tf" || strings.HasSuffix(name, "_override.tf") {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		contents[name] = content
	}
	return contents, nil
}

// parseDriftConfig parses the root's files of contents, without the files
// of the modules it calls it may hold (see isModuleFile).
func parseDriftConfig(contents map[string][]byte) (*driftConfig, error) {
	return reparseDriftConfig(nil, contents, nil)
}

// reparse returns the configuration of contents, as parseDriftConfig
// does, parsing again only the files whose content isn't what cfg parsed,
// as the edits since have left them; cfg itself where none is. Check
// edits one change at a time, each found in the files as the edits before
// left them, and an edit touches a file or two of the many.
func (cfg *driftConfig) reparse(contents map[string][]byte) (*driftConfig, error) {
	return reparseDriftConfig(cfg, contents, nil)
}

// reparseWith is reparse, taking the parse of a file from parses where
// it has one of the file's content, as the check of an edit leaves it.
func (cfg *driftConfig) reparseWith(contents map[string][]byte, parses parseCache) (*driftConfig, error) {
	return reparseDriftConfig(cfg, contents, parses)
}

func reparseDriftConfig(prev *driftConfig, contents map[string][]byte, parses parseCache) (*driftConfig, error) {
	parsed := map[string]*driftConfig{}
	changed := prev == nil
	for _, name := range slices.Sorted(maps.Keys(contents)) {
		if isModuleFile(name) {
			continue
		}
		if prev != nil {
			if file, ok := prev.parsed[name]; ok && bytes.Equal(file.files[name].Src, contents[name]) {
				parsed[name] = file
				continue
			}
		}
		f, err := parses.parse(name, contents[name])
		if err != nil {
			return nil, err
		}
		file, err := driftFileOf(name, f)
		if err != nil {
			return nil, err
		}
		parsed[name] = file
		changed = true
	}
	if !changed && len(parsed) == len(prev.parsed) {
		return prev, nil
	}
	cfg := newDriftConfig()
	cfg.parsed = parsed
	// In the order of the names, so that of two files that declare the
	// same, the later one's is kept.
	for _, name := range slices.Sorted(maps.Keys(parsed)) {
		cfg.add(parsed[name])
	}
	return cfg, nil
}

func newDriftConfig() *driftConfig {
	return &driftConfig{files: map[string]*hclfile.File{}, blocks: map[string]driftBlock{}, locals: map[string]driftLocal{}, uses: map[string]int{}, varUses: map[string]int{}, variables: map[string]*hclsyntax.Block{}, calls: map[string]driftBlock{}}
}

// driftFileOf is the configuration of the root's file name, parsed as f,
// alone.
func driftFileOf(name string, f *hclfile.File) (*driftConfig, error) {
	cfg := newDriftConfig()
	cfg.files[name] = f
	for _, b := range f.Syntax.Blocks {
		switch {
		case b.Type == "resource" && len(b.Labels) == 2:
			cfg.blocks[b.Labels[0]+"."+b.Labels[1]] = driftBlock{file: name, f: f, b: b}
		case b.Type == "locals":
			for n, attr := range b.Body.Attributes {
				cfg.locals[n] = driftLocal{file: name, f: f, attr: attr}
			}
		case b.Type == "variable" && len(b.Labels) == 1:
			cfg.variables[b.Labels[0]] = b
		case b.Type == "module" && len(b.Labels) == 1:
			cfg.calls[b.Labels[0]] = driftBlock{file: name, f: f, b: b}
		}
	}
	for _, t := range hclfile.Traversals(f.Syntax) {
		if t.RootName() != "local" || len(t) < 2 {
			continue
		}
		if step, ok := t[1].(hcl.TraverseAttr); ok {
			cfg.uses[step.Name]++
		}
	}
	for _, b := range f.Syntax.Blocks {
		if b.Type == "variable" || b.Type == "output" {
			continue
		}
		for _, t := range hclfile.Traversals(b.Body) {
			if t.RootName() != "var" || len(t) < 2 {
				continue
			}
			if step, ok := t[1].(hcl.TraverseAttr); ok {
				cfg.varUses[step.Name]++
			}
		}
	}
	return cfg, nil
}

// parseCache keeps the last parse of each file of an apply, by the name
// the file goes by, with the literal secrets it holds once asked for:
// apply checks each edited file, parsing it, and the next change is
// resolved in the same file, so that parse, and its findings, are kept
// for both rather than made again. A nil parseCache parses every time.
type parseCache map[string]*cachedParse

type cachedParse struct {
	f        *hclfile.File
	literals []string
	checked  bool
}

// parse returns the parse of content, the file name: the kept one where
// it is of the same content.
func (c parseCache) parse(name string, content []byte) (*hclfile.File, error) {
	if p, ok := c[name]; ok && bytes.Equal(p.f.Src, content) {
		return p.f, nil
	}
	f, err := hclfile.Parse(name, content)
	if err != nil {
		return nil, err
	}
	if c != nil {
		c[name] = &cachedParse{f: f}
	}
	return f, nil
}

// literals returns secret.Literals of f, which parse returned for name,
// once per parse.
func (c parseCache) literals(name string, f *hclfile.File) []string {
	p, ok := c[name]
	if !ok || p.f != f {
		return secret.Literals(f)
	}
	if !p.checked {
		p.literals, p.checked = secret.Literals(f), true
	}
	return p.literals
}

// add adds what other declares and refers to to cfg, over what cfg has.
func (cfg *driftConfig) add(other *driftConfig) {
	maps.Copy(cfg.files, other.files)
	maps.Copy(cfg.blocks, other.blocks)
	maps.Copy(cfg.locals, other.locals)
	maps.Copy(cfg.variables, other.variables)
	maps.Copy(cfg.calls, other.calls)
	for name, n := range other.uses {
		cfg.uses[name] += n
	}
	for name, n := range other.varUses {
		cfg.varUses[name] += n
	}
}

// variableFile returns the file that declares v, or nil.
func (cfg *driftConfig) variableFile(v *hclsyntax.Block) *namedFile {
	for name, f := range cfg.files {
		for _, b := range f.Syntax.Blocks {
			if b == v {
				return &namedFile{name: name, f: f}
			}
		}
	}
	return nil
}
