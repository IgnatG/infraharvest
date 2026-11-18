// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"errors"
	"math/big"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/logging"
)

// driftModule is a module a root calls, as Terraform installed it, read
// to trace a change of one of its resources back to the call.
type driftModule struct {
	// dir is the module's directory, relative to the root.
	dir string
	cfg *driftConfig
	// variables are the types of its variables that declare one, with
	// their optional attributes, by name.
	variables map[string]cty.Type
	// calls counts the module calls, of the root and of its modules, that
	// call the directory.
	calls int
	// inContents says it was parsed from the contents drift edits (see
	// syncModules), which have its files.
	inContents bool
}

// loadDriftModules reads the modules the root in dir calls, by their keys
// in the modules.json of its last init, whose working files are in
// dataDir, or .terraform in dir for "", such as logs, or net.vpc for a
// call inside a module: none if it calls none, or if they can't be read.
// It returns too why each module it couldn't read can't be, by key, and
// by "" why none can be, where modules.json can't be read, so that a
// change inside one says why it can't be traced; and it logs them.
func loadDriftModules(dir, dataDir string) (map[string]*driftModule, map[string]string) {
	unread := map[string]string{}
	installed, err := tfrun.InstalledModules(dir, dataDir)
	if err != nil {
		logging.Warnf("%s: drift: can't read the modules the root calls, so changes inside them can't be traced: %v", dir, err)
		unread[""] = "the modules the root calls can't be read: " + err.Error()
		return nil, unread
	}
	calls := map[string]int{}
	for _, m := range installed {
		calls[filepath.Clean(m.Dir)]++
	}
	modules := map[string]*driftModule{}
	for _, m := range installed {
		contents, err := tfContents(m.Dir)
		if err != nil {
			logging.Warnf("%s: drift: can't read module %s, so changes inside it can't be traced: %v", dir, m.Key, err)
			unread[m.Key] = "the module's files can't be read: " + err.Error()
			continue
		}
		if len(contents) == 0 {
			continue
		}
		cfg, err := parseDriftConfig(contents)
		if err != nil {
			logging.Warnf("%s: drift: module %s doesn't parse, so changes inside it can't be traced: %v", dir, m.Key, err)
			unread[m.Key] = "the module doesn't parse: " + firstDiagnostic(err)
			continue
		}
		mod := newDriftModule(cfg)
		mod.dir = filepath.ToSlash(m.Dir)
		if rel, err := filepath.Rel(dir, m.Dir); err == nil {
			mod.dir = filepath.ToSlash(rel)
		}
		mod.calls = calls[filepath.Clean(m.Dir)]
		modules[m.Key] = mod
	}
	return modules, unread
}

// firstDiagnostic is err, an error of a parse, as its first diagnostic
// alone: the rest may be many, and a reason needs only why.
func firstDiagnostic(err error) string {
	var diags hcl.Diagnostics
	if errors.As(err, &diags) && len(diags) > 0 {
		return diags[0].Error()
	}
	return err.Error()
}

// newDriftModule is the module of cfg, its files parsed.
func newDriftModule(cfg *driftConfig) *driftModule {
	m := &driftModule{cfg: cfg, variables: map[string]cty.Type{}, calls: 1}
	for name, b := range cfg.variables {
		attr, ok := b.Body.Attributes["type"]
		if !ok {
			continue
		}
		if ty, _, diags := typeexpr.TypeConstraintWithDefaults(attr.Expr); !diags.HasErrors() {
			m.variables[name] = ty
		}
	}
	return m
}

// instanceKey is the instance key of a resource or a module call: a
// for_each key, a count index, or none.
type instanceKey struct {
	key           string
	index         int
	hasKey, isNum bool
}

// moduleStep is a module call of a resource's address, with its instance
// key.
type moduleStep struct {
	name string
	key  instanceKey
}

// splitModuleAddress splits the address of a resource in a module call,
// such as module.logs["a.b"].aws_s3_bucket.this[0], into its calls, from
// the root's, the resource's address in the innermost module without its
// instance key, such as aws_s3_bucket.this, and that key; false for an
// address in no module call, or one it can't read.
func splitModuleAddress(address string) ([]moduleStep, string, instanceKey, bool) {
	t, diags := hclsyntax.ParseTraversalAbs([]byte(address), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, "", instanceKey{}, false
	}
	name := func(i int) string {
		if i >= len(t) {
			return ""
		}
		switch s := t[i].(type) {
		case hcl.TraverseRoot:
			return s.Name
		case hcl.TraverseAttr:
			return s.Name
		}
		return ""
	}
	key := func(i int) (instanceKey, bool) {
		if i >= len(t) {
			return instanceKey{}, false
		}
		idx, ok := t[i].(hcl.TraverseIndex)
		if !ok {
			return instanceKey{}, false
		}
		return keyOf(idx.Key)
	}
	var calls []moduleStep
	i := 0
	for name(i) == "module" && name(i+1) != "" {
		call := moduleStep{name: name(i + 1)}
		i += 2
		if k, ok := key(i); ok {
			call.key = k
			i++
		}
		calls = append(calls, call)
	}
	typ, n := name(i), name(i+1)
	if len(calls) == 0 || typ == "" || n == "" || typ == "data" {
		return nil, "", instanceKey{}, false
	}
	var k instanceKey
	switch {
	case i+2 == len(t):
	case i+3 == len(t):
		var ok bool
		if k, ok = key(i + 2); !ok {
			return nil, "", instanceKey{}, false
		}
	default:
		return nil, "", instanceKey{}, false
	}
	return calls, typ + "." + n, k, true
}

// keyOf returns an instance key's value as an instanceKey.
func keyOf(v cty.Value) (instanceKey, bool) {
	switch {
	case !v.IsKnown() || v.IsNull():
		return instanceKey{}, false
	case v.Type() == cty.String:
		return instanceKey{key: v.AsString(), hasKey: true}, true
	case v.Type() == cty.Number:
		i, accuracy := v.AsBigFloat().Int64()
		if accuracy != big.Exact || i < 0 {
			return instanceKey{}, false
		}
		return instanceKey{index: int(i), isNum: true}, true
	}
	return instanceKey{}, false
}

// moduleKey is the key modules.json has the innermost of calls by, such
// as net.vpc.
func moduleKey(calls []moduleStep) string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.name
	}
	return strings.Join(names, ".")
}

// unkeyedAddress returns the address of the block of the resource at
// address, without the instance keys of the resource and of the module
// calls it is in, such as module.logs.aws_s3_bucket.this for
// module.logs["a"].aws_s3_bucket.this[0].
func unkeyedAddress(address string) string {
	calls, resource, _, ok := splitModuleAddress(address)
	if !ok {
		return hclfile.TrimInstanceKey(address)
	}
	var b strings.Builder
	for _, c := range calls {
		b.WriteString("module." + c.name + ".")
	}
	return b.String() + resource
}

// curatedAdapter returns a copy of the adapter of list, the run's (see
// Options.Adapters), whose module a module call calls, or nil. It
// matches a call as generation writes it: the adapter's source, at the
// adapter's version, or a newer one, which --modules latest-untested
// calls with the same adapter. Only an adapter with an Inverse traces a
// change into its call: the others, such as those of the organisation's
// own modules, leave the call to the module's code (see moduleTracer).
func curatedAdapter(b *hclsyntax.Block, list []adapters.Adapter) *adapters.Adapter {
	source, called := hclfile.StringAttribute(b.Body, "source"), hclfile.StringAttribute(b.Body, "version")
	if source == "" {
		return nil
	}
	for _, a := range list {
		if len(a.Inverse) > 0 && a.Source == source && callsAtLeast(called, a.Version) {
			return &a
		}
	}
	return nil
}

// callsAtLeast reports whether a call of a module at version called calls
// the release tested, or a newer one; any, for an adapter that names no
// release.
func callsAtLeast(called, tested string) bool {
	if tested == "" || called == tested {
		return true
	}
	c, err := version.NewVersion(called)
	if err != nil {
		return false
	}
	t, err := version.NewVersion(tested)
	return err == nil && !c.LessThan(t)
}

// hasInstances reports whether a block makes several instances, with
// count or for_each.
func hasInstances(b *hclsyntax.Block) bool {
	_, count := b.Body.Attributes["count"]
	_, forEach := b.Body.Attributes["for_each"]
	return count || forEach
}
