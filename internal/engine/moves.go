// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// MovesFileName holds the moved blocks of a root generated again: a plan
// on the root's state then shows each resource at another address than
// before as moved, rather than destroyed and created there. It also holds
// the removed blocks of the resources the root hands over to other roots
// (see Removal).
const MovesFileName = "moves.tf"

// Move is a moved block: the resource at From is at To now.
type Move struct {
	From, To string
}

// Removal is a removed block, with destroy = false: the root's state
// forgets the resource or module call at From, without destroying it, as
// another root of the output imports what it has (see Arrival).
type Removal struct {
	From string
	// Resources are what From has, each with the root it goes to; none
	// for a removed block of moves.tf whose resources no checkpoint says.
	Resources []Departure `json:",omitempty"`
}

// Retype is a resource of a root generated again that is of another type
// now, another name of the one it was (see AliasTypes), with the same
// import ID, such as an aws_alb that is an aws_lb: Type and From are the
// type and address the root had it at, and To where it imports it now.
// No moved block can follow it, as the providers don't move state from
// one type to another, so a removed block, with destroy = false, makes
// the root's state forget it, and its import block imports it again.
type Retype struct {
	Type, ID string
	From, To string
	// Removed is what the removed block takes: From without its instance
	// key, when all of that resource is of the other type now; else, as a
	// removed block takes no instance keys and a whole module call, a
	// resource of the root alone that a moved block moves From to first
	// (see DepartedAddress).
	Removed string
}

// Departure is a resource a root hands over to another root: Address is
// where the root has it, and Root the other root, relative to the output
// directory, with forward slashes.
type Departure struct {
	Type, ID      string
	Address, Root string
}

// Arrival is a resource a root imports that another root hands over (see
// Removal): Root is that root, relative to the output directory, with
// forward slashes, From where it has the resource, and To where this root
// imports it, once Generate has generated it.
type Arrival struct {
	Type, ID       string
	Root, From, To string
	// FromType, if set, is the type Root has the resource as, another
	// name of Type (see AliasTypes): its removed block forgets it as
	// that, and this root imports it as Type.
	FromType string `json:",omitempty"`
}

// HadType returns the type the root a hands the resource over from has
// it as: FromType, or else Type.
func (a Arrival) HadType() string {
	if a.FromType != "" {
		return a.FromType
	}
	return a.Type
}

// RemovedFrom returns what a removed block takes to forget the resource
// at address: the resource without its instance key, as aws_subnet.a for
// aws_subnet.a[0], or, for one in a module call, the call, as module.vpc
// for module.vpc.aws_subnet.a[0]. A removed block takes no instance keys.
func RemovedFrom(address string) string {
	if rest, ok := strings.CutPrefix(address, "module."); ok {
		name, _, _ := strings.Cut(rest, ".")
		if i := strings.IndexByte(name, '['); i >= 0 {
			name = name[:i]
		}
		return "module." + name
	}
	if i := strings.IndexByte(address, '['); i >= 0 {
		return address[:i]
	}
	return address
}

// removals returns the removed blocks of a root generated again, given
// now, where its resources are: previous's own removed blocks, but for
// those whose resource or module call it has again, which it would forget
// otherwise.
func removals(previous *Previous, now []Managed) []Removal {
	if previous == nil {
		return nil
	}
	declared := map[string]bool{}
	for _, m := range now {
		declared[RemovedFrom(m.Address)] = true
	}
	var removed []Removal
	seen := map[string]bool{}
	for _, r := range previous.Removed {
		if declared[r.From] || seen[r.From] {
			continue
		}
		seen[r.From] = true
		removed = append(removed, r)
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i].From < removed[j].From })
	return removed
}

// arrived returns the arrivals of arrivals the root imports, by now,
// where it imports its resources, with where that is.
func arrived(arrivals []Arrival, now []Managed) []Arrival {
	at := map[External]string{}
	for _, m := range now {
		at[External{Type: m.Type, ID: m.ID}] = m.Address
	}
	var got []Arrival
	for _, a := range arrivals {
		if to, ok := at[External{Type: a.Type, ID: a.ID}]; ok {
			a.To = to
			got = append(got, a)
		}
	}
	sort.Slice(got, func(i, j int) bool { return got[i].To < got[j].To })
	return got
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
	// Removed are the removed blocks the root gets: those of its moves.tf,
	// which a plan may not have applied yet either, and those of the
	// resources it now hands over to other roots.
	Removed []Removal
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
// those p's resources had, each for itself, and those p's moved and
// removed blocks name, for none.
func (p *Previous) reserved() map[string]External {
	if p == nil {
		return nil
	}
	reserved := map[string]External{}
	var named []string
	for _, m := range p.Moved {
		named = append(named, m.From, m.To)
	}
	for _, r := range p.Removed {
		named = append(named, r.From)
	}
	for _, address := range named {
		if typ, label, ok := rootResource(address); ok {
			reserved[typ+"."+label] = External{}
		}
	}
	// A resource a removed block hands over that comes back keeps its
	// address, and the block goes (see removals).
	for _, r := range p.Removed {
		if typ, label, ok := rootResource(r.From); ok && len(r.Resources) == 1 && r.Resources[0].Address == r.From {
			reserved[typ+"."+label] = External{Type: r.Resources[0].Type, ID: r.Resources[0].ID}
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
// previous now at another address; and the resources of previous now of
// another type (see retypes). It leaves out, with why, the moves
// Terraform rejects: into or out of a call that a module from the
// registry the root calls makes, as moved blocks stay within a module
// package, and onto an address another resource had. It returns none if
// the blocks together, with those that move what removed blocks take
// alone first (see departureMoves), don't move each resource once: two
// that move the same one, or to the same address, a chain that comes
// back to where it started, or a move from an address a resource has
// now.
func moves(previous *Previous, now []Managed, sources map[string]string) ([]Move, []Retype, []string) {
	if previous == nil {
		return nil, nil, nil
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
		case packagedCall(p.Address, sources) != "":
			unmoved = append(unmoved, fmt.Sprintf("%s is now %s, and a moved block can't move it out of a call %s makes", p.Address, to, packagedCall(p.Address, sources)))
		case packagedCall(to, sources) != "":
			unmoved = append(unmoved, fmt.Sprintf("%s is now %s, and a moved block can't move it into a call %s makes", p.Address, to, packagedCall(to, sources)))
		case held[to]:
			unmoved = append(unmoved, fmt.Sprintf("%s is now %s, where another of its resources was", p.Address, to))
		default:
			moved = append(moved, Move{From: p.Address, To: to})
		}
	}
	retyped, why := retypes(previous, now, sources)
	unmoved = append(unmoved, why...)
	if problem := conflictingMoves(append(slices.Clone(moved), departureMoves(retyped)...), declared); problem != "" {
		if len(previous.Moved) > 0 {
			problem += fmt.Sprintf(": plan and apply the root, and delete %s, before generating it again", MovesFileName)
		}
		return nil, nil, append(unmoved, problem)
	}
	sortMoves(moved)
	return moved, retyped, unmoved
}

// retypes returns the resources of previous that now, where the root's
// resources are, has as another name of their type with the same import
// ID (see AliasTypes), with the removed block that forgets each (see
// Retype.Removed), sorted by where the root had them. It leaves out,
// with why, those it can't forget: in a call that a module from the
// registry the root calls makes, by sources, which a moved block can't
// reach, and those whose addresses are only inferred (see
// Managed.Unverified). Resources now of other types with the same ID are
// left to Retyped.
func retypes(previous *Previous, now []Managed, sources map[string]string) ([]Retype, []string) {
	at := map[External]string{}
	taken := map[string]bool{}
	declared := map[string]bool{}
	for _, m := range now {
		at[External{Type: m.Type, ID: m.ID}] = m.Address
		declared[RemovedFrom(m.Address)] = true
		addTaken(taken, m.Address)
	}
	members := map[string]int{}
	for _, p := range previous.Addresses {
		members[RemovedFrom(p.Address)]++
		addTaken(taken, p.Address)
	}
	for _, m := range previous.Moved {
		addTaken(taken, m.From)
		addTaken(taken, m.To)
	}
	for _, r := range previous.Removed {
		addTaken(taken, r.From)
	}
	var retyped []Retype
	var why []string
	byFrom := map[string]int{}
	for _, p := range previous.Addresses {
		if _, ok := at[External{Type: p.Type, ID: p.ID}]; ok {
			continue
		}
		for _, typ := range AliasTypes(p.Type) {
			to, ok := at[External{Type: typ, ID: p.ID}]
			if !ok {
				continue
			}
			switch {
			case p.Unverified:
				why = append(why, fmt.Sprintf("%s is now %s, another name of its type, but where the root has it is only inferred, as its import blocks are deleted", p.Address, to))
			case packagedCall(p.Address, sources) != "":
				why = append(why, fmt.Sprintf("%s is now %s, another name of its type, and a moved block can't move it out of a call %s makes for a removed block to take it alone", p.Address, to, packagedCall(p.Address, sources)))
			default:
				retyped = append(retyped, Retype{Type: p.Type, ID: p.ID, From: p.Address, To: to})
				byFrom[RemovedFrom(p.Address)]++
			}
			break
		}
	}
	sort.Slice(retyped, func(i, j int) bool { return retyped[i].From < retyped[j].From })
	for i, r := range retyped {
		from := RemovedFrom(r.From)
		if !strings.HasPrefix(from, "module.") && byFrom[from] == members[from] && !declared[from] {
			retyped[i].Removed = from
			continue
		}
		retyped[i].Removed = DepartedAddress(r.From, taken)
	}
	return retyped, why
}

// addTaken adds the resource of the root that address names, as
// type.label, to taken; nothing for one in a module call.
func addTaken(taken map[string]bool, address string) {
	if typ, label, ok := rootResource(address); ok {
		taken[typ+"."+label] = true
	}
}

// departureMoves returns the moved blocks that move what the removed
// blocks of retyped take alone to the resources of the root those blocks
// take (see Retype.Removed), sorted.
func departureMoves(retyped []Retype) []Move {
	var moved []Move
	for _, r := range retyped {
		if r.Removed != RemovedFrom(r.From) {
			moved = append(moved, Move{From: r.From, To: r.Removed})
		}
	}
	sortMoves(moved)
	return slices.Compact(moved)
}

// DepartedAddress returns the address a moved block moves address, an
// instance of a resource or a resource in a module call, to for a
// removed block to take it alone (see Retype.Removed): a resource of
// the root of its type, labelled departed_ and what address names, such
// as aws_subnet.departed_vpc_a_x for module.vpc.aws_subnet.a["x"], that
// taken, the root's resources as type.label, doesn't have yet. It adds
// the address to taken.
func DepartedAddress(address string, taken map[string]bool) string {
	var names []string
	rest := address
	for {
		after, ok := strings.CutPrefix(rest, "module.")
		if !ok {
			break
		}
		end := strings.IndexAny(after, ".[")
		if end < 0 {
			break
		}
		names = append(names, after[:end])
		rest = after[end:]
		if strings.HasPrefix(rest, "[") {
			tail := afterKey(rest)
			names = append(names, rest[:len(rest)-len(tail)])
			rest = tail
		}
		rest = strings.TrimPrefix(rest, ".")
	}
	typ, label, _ := strings.Cut(rest, ".")
	base := typ + ".departed_" + identifier(strings.Join(append(names, label), "_"))
	departed := base
	for n := 2; taken[departed]; n++ {
		departed = fmt.Sprintf("%s_%d", base, n)
	}
	taken[departed] = true
	return departed
}

// identifier returns s with each run of underscores and characters a
// label can't have, such as the brackets and quotes of an instance key,
// as one underscore, and none at either end.
func identifier(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range s {
		label := r == '-' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
		if !label {
			gap = true
			continue
		}
		if gap && b.Len() > 0 {
			b.WriteByte('_')
		}
		gap = false
		b.WriteRune(r)
	}
	return b.String()
}

// isModuleCall reports whether address is a module call alone, such as
// module.logs.
func isModuleCall(address string) bool {
	return strings.HasPrefix(address, "module.") && strings.Count(address, ".") == 1
}

// packagedCall returns, for address in a module call that a call the
// root makes of a module that isn't a local one makes in turn, such as
// module.eks.module.kms.aws_kms_key.this[0], the root's call and its
// source, as module.eks (terraform-aws-modules/eks/aws); "" otherwise.
// The inner call is in that module's package, so a moved block of the
// root can't reach it; a resource of the root's call itself, such as
// module.vpc.aws_vpc.this[0], it can. A call the root no longer makes
// counts as local.
func packagedCall(address string, sources map[string]string) string {
	rest, ok := strings.CutPrefix(address, "module.")
	if !ok {
		return ""
	}
	end := strings.IndexAny(rest, ".[")
	if end < 0 {
		return ""
	}
	name := rest[:end]
	rest = rest[end:]
	if strings.HasPrefix(rest, "[") {
		if rest = afterKey(rest); rest == "" {
			return ""
		}
	}
	source, ok := sources[name]
	if !ok || !strings.HasPrefix(rest, ".module.") || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return ""
	}
	return fmt.Sprintf("module.%s (%s)", name, source)
}

// afterKey returns what follows the instance key that s starts with, such
// as `["a.b"]`, keys in quotes taking any character but an unescaped
// quote; "" if s doesn't end the key.
func afterKey(s string) string {
	quoted := false
	for i := 1; i < len(s); i++ {
		switch {
		case quoted && s[i] == '\\':
			i++
		case s[i] == '"':
			quoted = !quoted
		case !quoted && s[i] == ']':
			return s[i+1:]
		}
	}
	return ""
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

// writeMoves writes moved into dir's moves.tf, with the moves of what
// the removed blocks of retyped take alone (see
// departureMoves), sorted by where they move to, then those removed
// blocks, sorted by what they remove, each with destroy = false; it
// removes the file if there are none.
func writeMoves(dir string, moved []Move, removed []Removal, retyped []Retype) error {
	path := filepath.Join(dir, MovesFileName)
	if len(moved) == 0 && len(removed) == 0 && len(retyped) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	sorted := append(slices.Clone(moved), departureMoves(retyped)...)
	sortMoves(sorted)
	var b strings.Builder
	for i, m := range sorted {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "moved {\n  from = %s\n  to = %s\n}\n", m.From, m.To)
	}
	froms := make([]string, 0, len(removed))
	for _, r := range removed {
		froms = append(froms, r.From)
	}
	for _, r := range retyped {
		froms = append(froms, r.Removed)
	}
	sort.Strings(froms)
	for i, from := range slices.Compact(froms) {
		if i > 0 || len(sorted) > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "removed {\n  from = %s\n\n  lifecycle {\n    destroy = false\n  }\n}\n", from)
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

// ReadRemoved returns the removed blocks of dir's moves.tf, by what they
// remove; none if dir has no moves.tf.
func ReadRemoved(dir string) ([]Removal, error) {
	f, err := loadOptionalHCL(filepath.Join(dir, MovesFileName))
	if err != nil || f == nil {
		return nil, err
	}
	var removed []Removal
	for _, b := range f.syntax.Blocks {
		from, ok := b.Body.Attributes["from"]
		if b.Type != "removed" || !ok {
			continue
		}
		removed = append(removed, Removal{From: strings.TrimSpace(string(from.Expr.Range().SliceBytes(f.src)))})
	}
	return removed, nil
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

// Vacate writes the root in dir that an earlier import generated, all of
// whose resources go to other roots now (see Removal): opts.Config, such
// as versions.tf and providers.tf, moves.tf with the removed blocks of
// opts.Previous, and a README that says to apply the root before the
// roots they go to. A plan of it then forgets them, and destroys
// nothing. It runs no Terraform: the gate is the moves check (see
// checkMoves). It fails if opts.Previous has no removed blocks, or still
// moves resources.
func Vacate(dir string, opts Options) (*Result, error) {
	if opts.Previous == nil || len(opts.Previous.Removed) == 0 {
		return nil, errors.New("no resources to hand over")
	}
	if len(opts.Previous.Moved) > 0 {
		return nil, fmt.Errorf("%s still moves resources: plan and apply the root, and delete %s, before generating it again", MovesFileName, MovesFileName)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for name, content := range opts.Config {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return nil, err
		}
	}
	result := &Result{Removed: removals(opts.Previous, nil)}
	if err := writeMoves(dir, nil, result.Removed, nil); err != nil {
		return nil, err
	}
	if err := requireMinimumVersion(dir); err != nil {
		return nil, err
	}
	check, err := checkMoves(dir)
	if err != nil {
		return nil, err
	}
	result.Gate = Gate{check}
	return result, WriteReadme(dir, result)
}
