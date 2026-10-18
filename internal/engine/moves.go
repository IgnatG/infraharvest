// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// MovesFileName holds the moved blocks of a root generated again: a plan
// on the root's state then shows each resource at another address than
// before as moved, rather than destroyed and created there.
const MovesFileName = "moves.tf"

// Move is a moved block: the resource at From is at To now.
type Move struct {
	From, To string
}

// Previous is what an earlier import generated in a root Generate
// generates again (see Options.Previous).
type Previous struct {
	// Addresses are where that import put the root's resources.
	Addresses []Managed
	// Labels are the labels it gave them, by type and import ID, such as
	// those of resources it then moved into module calls. A resource
	// without one has the label of its address, if that is in the root.
	Labels map[External]string
	// Moved are the moved blocks of the root's moves.tf, which a plan may
	// not have applied yet: Generate keeps them, and its own moves follow
	// on from them.
	Moved []Move
}

// labels returns the label p says each resource had, by type and import
// ID (see Previous.Labels).
func (p *Previous) labels() map[External]string {
	if p == nil {
		return nil
	}
	labels := map[External]string{}
	for _, m := range p.Addresses {
		if label, ok := rootLabel(m.Address); ok {
			labels[External{Type: m.Type, ID: m.ID}] = label
		}
	}
	for key, label := range p.Labels {
		labels[key] = label
	}
	return labels
}

// reserved returns the resource addresses of the root, as type.label,
// that a resource Generate labels may take only if it is the one given:
// those p's resources had, each for itself, and those p's moved blocks
// name, for none.
func (p *Previous) reserved() map[string]External {
	if p == nil {
		return nil
	}
	reserved := map[string]External{}
	for _, m := range p.Moved {
		for _, address := range []string{m.From, m.To} {
			if typ, label, ok := rootResource(address); ok {
				reserved[typ+"."+label] = External{}
			}
		}
	}
	for _, m := range p.Addresses {
		if label, ok := rootLabel(m.Address); ok {
			reserved[m.Type+"."+label] = External{Type: m.Type, ID: m.ID}
		}
	}
	return reserved
}

// rootResource returns the type and label of address, a resource of the
// root such as aws_s3_bucket.logs or aws_s3_bucket.logs[0]; false for one
// in a module call.
func rootResource(address string) (string, string, bool) {
	if strings.HasPrefix(address, "module.") {
		return "", "", false
	}
	typ, label, ok := strings.Cut(address, ".")
	if i := strings.IndexByte(label, '['); i >= 0 {
		label = label[:i]
	}
	return typ, label, ok && label != ""
}

// rootLabel returns the label of address, if it is a resource of the root.
func rootLabel(address string) (string, bool) {
	_, label, ok := rootResource(address)
	return label, ok
}

// moves returns the moved blocks of a root generated again, given now,
// where its resources are, and sources, the sources of its module calls
// by name: previous's own moved blocks, then one for each resource of
// previous now at another address. It leaves out, with why, the moves
// Terraform rejects: into a module call or out of one it still makes
// whose module isn't a local one, as moved blocks stay within a module
// package, and onto an address another resource had. It returns none if
// the blocks together don't move each resource once: two that move the
// same one, or to the same address, a chain that comes back to where it
// started, or a move from an address a resource has now.
func moves(previous *Previous, now []Managed, sources map[string]string) ([]Move, []string) {
	if previous == nil {
		return nil, nil
	}
	at := map[External]string{}
	declared := map[string]bool{}
	for _, m := range now {
		at[External{Type: m.Type, ID: m.ID}] = m.Address
		declared[m.Address] = true
	}
	held := map[string]bool{}
	for _, p := range previous.Addresses {
		held[p.Address] = true
	}
	moved := append([]Move(nil), previous.Moved...)
	var unmoved []string
	for _, p := range previous.Addresses {
		to, ok := at[External{Type: p.Type, ID: p.ID}]
		// Of an address an older release recorded as its module call alone,
		// no moved block can start: see Readdressed.
		if !ok || to == p.Address || (p.Unverified && isModuleCall(p.Address)) {
			continue
		}
		switch {
		case remoteCall(p.Address, sources) != "":
			unmoved = append(unmoved, fmt.Sprintf("%s is now %s, and a moved block can't move it out of %s", p.Address, to, remoteCall(p.Address, sources)))
		case remoteCall(to, sources) != "":
			unmoved = append(unmoved, fmt.Sprintf("%s is now %s, and a moved block can't move it into %s", p.Address, to, remoteCall(to, sources)))
		case held[to]:
			unmoved = append(unmoved, fmt.Sprintf("%s is now %s, where another of its resources was", p.Address, to))
		default:
			moved = append(moved, Move{From: p.Address, To: to})
		}
	}
	if problem := conflictingMoves(moved, declared); problem != "" {
		if len(previous.Moved) > 0 {
			problem += fmt.Sprintf(": plan and apply the root, and delete %s, before generating it again", MovesFileName)
		}
		return nil, append(unmoved, problem)
	}
	sortMoves(moved)
	return moved, unmoved
}

// isModuleCall reports whether address is a module call alone, such as
// module.logs.
func isModuleCall(address string) bool {
	return strings.HasPrefix(address, "module.") && strings.Count(address, ".") == 1
}

// remoteCall returns, for address in a module call the root makes, such
// as module.vpc.aws_vpc.this[0], the call and its source if the module
// isn't a local one, as module.vpc (terraform-aws-modules/vpc/aws); ""
// otherwise. A call the root no longer makes counts as local: Terraform
// lets a moved block take a resource out of it.
func remoteCall(address string, sources map[string]string) string {
	rest, ok := strings.CutPrefix(address, "module.")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, ".")
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	source, ok := sources[name]
	if !ok || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return ""
	}
	return fmt.Sprintf("module.%s (%s)", name, source)
}

// callSources returns the sources of the module calls of calls, by name.
func callSources(calls []ModuleCall) map[string]string {
	sources := map[string]string{}
	for _, c := range calls {
		if c.Declined == "" && c.Name != "" {
			sources[c.Name] = c.Source
		}
	}
	return sources
}

// conflictingMoves returns what of moved, moved blocks of a root that
// declares the addresses of declared, Terraform would reject; "" if
// nothing.
func conflictingMoves(moved []Move, declared map[string]bool) string {
	to := map[string]string{}
	into := map[string]bool{}
	for _, m := range moved {
		switch {
		case to[m.From] != "":
			return fmt.Sprintf("%s moves both to %s and to %s", m.From, to[m.From], m.To)
		case into[m.To]:
			return fmt.Sprintf("more than one resource moves to %s", m.To)
		case declared[m.From]:
			return fmt.Sprintf("%s moves to %s, but a resource is at %s still", m.From, m.To, m.From)
		}
		to[m.From] = m.To
		into[m.To] = true
	}
	for _, m := range moved {
		seen := map[string]bool{m.From: true}
		for next := m.To; next != ""; next = to[next] {
			if seen[next] {
				return fmt.Sprintf("the moves from %s come back to %s", m.From, next)
			}
			seen[next] = true
		}
	}
	return ""
}

func sortMoves(moved []Move) {
	sort.Slice(moved, func(i, j int) bool {
		if moved[i].To != moved[j].To {
			return moved[i].To < moved[j].To
		}
		return moved[i].From < moved[j].From
	})
}

// writeMoves writes moved into dir's moves.tf, sorted by where they move
// to; it removes the file if there are none.
func writeMoves(dir string, moved []Move) error {
	path := filepath.Join(dir, MovesFileName)
	if len(moved) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	sorted := append([]Move(nil), moved...)
	sortMoves(sorted)
	var b strings.Builder
	for i, m := range sorted {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "moved {\n  from = %s\n  to = %s\n}\n", m.From, m.To)
	}
	content := hclwrite.Format([]byte(b.String()))
	if _, diags := hclsyntax.ParseConfig(content, path, hcl.InitialPos); diags.HasErrors() {
		return fmt.Errorf("%s: %w", MovesFileName, diags)
	}
	return os.WriteFile(path, content, 0o644)
}

// ReadMoves returns the moved blocks of dir's moves.tf; none if dir has
// no moves.tf.
func ReadMoves(dir string) ([]Move, error) {
	f, err := loadOptionalHCL(filepath.Join(dir, MovesFileName))
	if err != nil || f == nil {
		return nil, err
	}
	var moved []Move
	for _, b := range f.syntax.Blocks {
		from, hasFrom := b.Body.Attributes["from"]
		to, hasTo := b.Body.Attributes["to"]
		if b.Type != "moved" || !hasFrom || !hasTo {
			continue
		}
		moved = append(moved, Move{
			From: strings.TrimSpace(string(from.Expr.Range().SliceBytes(f.src))),
			To:   strings.TrimSpace(string(to.Expr.Range().SliceBytes(f.src))),
		})
	}
	return moved, nil
}

// movedFrom returns the name of the root (see Names) that b, a moved or
// removed block, takes a resource from: type.label for a resource of the
// root, or module.name for one in a module call.
func movedFrom(b *hclsyntax.Block) (string, bool) {
	attr, ok := b.Body.Attributes["from"]
	if !ok {
		return "", false
	}
	traversal, diags := hcl.AbsTraversalForExpr(attr.Expr)
	if diags.HasErrors() || len(traversal) < 2 {
		return "", false
	}
	next, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return "", false
	}
	if traversal.RootName() == "module" {
		return "module." + next.Name, true
	}
	return traversal.RootName() + "." + next.Name, true
}
