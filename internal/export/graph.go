// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/report"
)

// The kinds of a graph's nodes.
const (
	nodeResource = "resource"
	nodeModule   = "module"
)

// The sources a module node may name: a registry module's address, or
// one of these, for a module whose source is a path or a URL, which can
// name hosts and directories the export doesn't share.
const (
	sourceLocal = "local"
	sourceOther = "other"
)

// graphNode is a node of the graph: an imported resource, named as the
// inventory names it (type, ID, account, region), or a module call, named
// by its source alone. Parent is the index of the module call a resource
// is in.
type graphNode struct {
	Kind    string `json:"kind"`
	Type    string `json:"type,omitempty"`
	ID      string `json:"id,omitempty"`
	Account string `json:"account"`
	Region  string `json:"region,omitempty"`
	Source  string `json:"source,omitempty"`
	Version string `json:"version,omitempty"`
	Parent  *int   `json:"parent,omitempty"`
}

// graphEdge says the node From refers to the node To: its configuration
// names it, directly or through a local or a data source.
type graphEdge struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type graphRedaction struct {
	IDsHashed bool   `json:"ids_hashed"`
	HashAlg   string `json:"hash_alg"`
}

type graphDoc struct {
	Schema      string         `json:"schema"`
	RunID       string         `json:"run_id"`
	GeneratedAt string         `json:"generated_at"`
	Tool        component      `json:"tool"`
	Cloud       string         `json:"cloud"`
	Nodes       []graphNode    `json:"nodes"`
	Edges       []graphEdge    `json:"edges"`
	Redaction   graphRedaction `json:"redaction"`
}

// registrySource matches a registry module's address: namespace/name/
// provider, with a registry host first for a private registry, such as
// terraform-aws-modules/vpc/aws.
var registrySource = regexp.MustCompile(`^(?:[a-z0-9.-]+\.[a-z]{2,}/)?[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9][A-Za-z0-9_-]*/[a-z0-9]+$`)

// shareableSource returns what the graph says of a module's source: the
// registry address, or local or other.
func shareableSource(source string) string {
	switch {
	case registrySource.MatchString(source):
		return source
	case strings.HasPrefix(source, "./"), strings.HasPrefix(source, "../"), filepath.IsAbs(source):
		return sourceLocal
	}
	return sourceOther
}

// graphRoot is what building the graph needs of one directory of the
// output: its scope, its imported resources by address, and its module
// calls by name.
type graphRoot struct {
	dir, scope string
	// resources has the node of each imported resource, by address with
	// its instance key, and bases the addresses of each, without it.
	resources map[string]int
	bases     map[string][]string
	modules   map[string]int
	merged    bool
}

// buildGraph returns the graph of the imported resources of r: a node for
// each, and for each module call, and an edge for each reference between
// them that the generated configuration of their root writes. It holds no
// addresses, names, paths, attributes or values: a resource is the
// inventory's type, ID, account and region, which --hash-ids hashes as it
// does there, so the two documents join on them.
func buildGraph(r *report.Report, opts Options, runID string, now time.Time) (*graphDoc, error) {
	red := newRedactor(nil, false, opts.HashKey)
	doc := &graphDoc{
		Schema:      schemaGraph,
		RunID:       runID,
		GeneratedAt: formatTime(now),
		Tool:        toComponent(r.Tool),
		Nodes:       []graphNode{},
		Edges:       []graphEdge{},
		Redaction:   graphRedaction{IDsHashed: opts.HashKey != nil, HashAlg: hashAlg},
	}
	var scopes []string
	var roots []*graphRoot
	for _, d := range r.Directories {
		if len(d.Imported) == 0 {
			continue
		}
		if d.Scope != "" && !slices.Contains(scopes, d.Scope) {
			scopes = append(scopes, d.Scope)
		}
		account, region := scopeParts(d.Scope)
		if account == "" {
			account = "unknown"
		}
		root := &graphRoot{dir: d.Path, scope: d.Scope, resources: map[string]int{}, bases: map[string][]string{}, modules: map[string]int{}, merged: d.MergedInto != ""}
		modules := slices.Clone(d.Modules)
		sort.Slice(modules, func(i, j int) bool { return modules[i].Name < modules[j].Name })
		for _, m := range modules {
			if m.Name == "" || m.Declined != "" {
				continue
			}
			root.modules[m.Name] = len(doc.Nodes)
			node := graphNode{Kind: nodeModule, Account: account, Region: region, Source: shareableSource(m.Source)}
			if node.Source != sourceLocal && node.Source != sourceOther {
				node.Version = m.Version
			}
			doc.Nodes = append(doc.Nodes, node)
		}
		imported := slices.Clone(d.Imported)
		sort.Slice(imported, func(i, j int) bool { return imported[i].Address < imported[j].Address })
		for _, res := range imported {
			typ := engine.AddressType(res.Address)
			if typ == "" || res.ID == "" {
				continue
			}
			if secret.CredentialFormat(res.ID) != "" {
				return nil, fmt.Errorf("a %s has an ID that looks like a credential: nothing is written", typ)
			}
			node := graphNode{Kind: nodeResource, Type: typ, ID: red.hash(res.ID), Account: account, Region: region}
			if call, ok := moduleCall(res.Address); ok {
				if i, ok := root.modules[call]; ok {
					node.Parent = &i
				}
			}
			root.resources[res.Address] = len(doc.Nodes)
			base := hclfile.TrimInstanceKey(res.Address)
			root.bases[base] = append(root.bases[base], res.Address)
			doc.Nodes = append(doc.Nodes, node)
		}
		roots = append(roots, root)
	}
	edges := map[graphEdge]bool{}
	for _, root := range roots {
		if root.merged {
			// Its configuration is in the root it was merged into, which
			// names its resources at other addresses (each environment's
			// instance, or residue): no edges, for now.
			continue
		}
		if err := root.edges(filepath.Join(opts.Out, filepath.FromSlash(root.dir)), edges); err != nil {
			return nil, err
		}
	}
	for e := range edges {
		doc.Edges = append(doc.Edges, e)
	}
	sort.Slice(doc.Edges, func(i, j int) bool {
		a, b := doc.Edges[i], doc.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	sort.Strings(scopes)
	cloud, err := cloudOf(r, scopes)
	if err != nil {
		return nil, err
	}
	doc.Cloud = cloud
	return doc, nil
}

// moduleCall returns the name of the module call, in the root, that the
// resource at address is in, if any.
func moduleCall(address string) (string, bool) {
	names := hclfile.AddressNames(address)
	if len(names) >= 4 && names[0] == "module" {
		return names[1], true
	}
	return "", false
}

// symbol is what a reference in configuration names: a resource, by its
// address without an instance key, a module call, a local, or a data
// source, the last two only on the way to the resources they name.
type symbol struct{ kind, name string }

// symbolOf returns what a traversal names.
func symbolOf(t hcl.Traversal) (symbol, bool) {
	name := func(i int) string {
		if i < len(t) {
			if a, ok := t[i].(hcl.TraverseAttr); ok {
				return a.Name
			}
		}
		return ""
	}
	switch t.RootName() {
	case "local":
		if n := name(1); n != "" {
			return symbol{"local", n}, true
		}
	case "module":
		if n := name(1); n != "" {
			return symbol{"module", n}, true
		}
	case "data":
		if typ, n := name(1), name(2); typ != "" && n != "" {
			return symbol{"data", typ + "." + n}, true
		}
	default:
		if a := hclfile.ResourceAddress(t); a != "" {
			return symbol{"resource", a}, true
		}
	}
	return symbol{}, false
}

// edges adds to edges the references between the nodes of the root whose
// configuration is in dir: from each resource block, and each module call,
// to each resource and module call its arguments name, directly or
// through locals and data sources.
func (root *graphRoot) edges(dir string, edges map[graphEdge]bool) error {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return err
	}
	// What each local and data source names, to follow them.
	via := map[symbol][]symbol{}
	type source struct {
		from    symbol
		targets []symbol
	}
	var sources []source
	refs := func(body *hclsyntax.Body) []symbol {
		var out []symbol
		for _, t := range hclfile.Traversals(body) {
			if s, ok := symbolOf(t); ok && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		return out
	}
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			switch {
			case b.Type == "locals":
				for name, attr := range b.Body.Attributes {
					var out []symbol
					for _, t := range attr.Expr.Variables() {
						if s, ok := symbolOf(t); ok && !slices.Contains(out, s) {
							out = append(out, s)
						}
					}
					via[symbol{"local", name}] = out
				}
			case b.Type == "data" && len(b.Labels) == 2:
				via[symbol{"data", b.Labels[0] + "." + b.Labels[1]}] = refs(b.Body)
			case b.Type == "resource" && len(b.Labels) == 2:
				sources = append(sources, source{from: symbol{"resource", b.Labels[0] + "." + b.Labels[1]}, targets: refs(b.Body)})
			case b.Type == "module" && len(b.Labels) == 1:
				sources = append(sources, source{from: symbol{"module", b.Labels[0]}, targets: refs(b.Body)})
			}
		}
	}
	for _, s := range sources {
		for _, target := range follow(s.targets, via) {
			tos := root.nodesOf(target)
			for _, from := range root.nodesOf(s.from) {
				for _, to := range matching(from, tos) {
					if from.index != to.index {
						edges[graphEdge{From: from.index, To: to.index}] = true
					}
				}
			}
		}
	}
	return nil
}

// matching returns the instances of tos that from refers to: the one with
// from's instance key, as each.key and count.index make a resource's
// instances refer to the same instance of another, or every one when none
// has it, as a reference to the whole resource does.
func matching(from instanceNode, tos []instanceNode) []instanceNode {
	if from.key != "" {
		for _, to := range tos {
			if to.key == from.key {
				return []instanceNode{to}
			}
		}
	}
	return tos
}

// follow follows locals and data sources in symbols to the resources and
// module calls they name, each once.
func follow(symbols []symbol, via map[symbol][]symbol) []symbol {
	var out []symbol
	seen := map[symbol]bool{}
	var walk func(s symbol)
	walk = func(s symbol) {
		if seen[s] {
			return
		}
		seen[s] = true
		switch s.kind {
		case "local", "data":
			for _, next := range via[s] {
				walk(next)
			}
		default:
			out = append(out, s)
		}
	}
	for _, s := range symbols {
		walk(s)
	}
	return out
}

// instanceNode is a node and the instance key of the resource it is, or
// "" for a resource without one, or a module call.
type instanceNode struct {
	index int
	key   string
}

// nodesOf returns the nodes s names in the root: each instance of a
// resource, or the module call.
func (root *graphRoot) nodesOf(s symbol) []instanceNode {
	switch s.kind {
	case "module":
		if i, ok := root.modules[s.name]; ok {
			return []instanceNode{{index: i}}
		}
	case "resource":
		var out []instanceNode
		for _, address := range root.bases[s.name] {
			out = append(out, instanceNode{index: root.resources[address], key: strings.TrimPrefix(address, s.name)})
		}
		return out
	}
	return nil
}
