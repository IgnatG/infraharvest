// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package rootfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/fsutil"
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
	// Instance, if set, is where the root has the one resource From
	// takes: an instance of a resource, or a resource of a module call,
	// others of which stay. As a removed block takes no instance keys and
	// only a whole module call, a moved block moves it to From, a
	// resource of the root alone (see engine.DepartedAddress), first.
	Instance string `json:",omitempty"`
}

// Retype is a resource of a root generated again that is of another type
// now, another name of the one it was (see tftypes.Aliases), with the same
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
	// (see engine.DepartedAddress).
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
// imports it, once engine.Generate has generated it.
type Arrival struct {
	Type, ID       string
	Root, From, To string
	// FromType, if set, is the type Root has the resource as, another
	// name of Type (see tftypes.Aliases): its removed block forgets it as
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

// DepartureMoves returns the moved blocks that move what the removed
// blocks of removed and retyped take alone to the resources of the root
// those blocks take (see Removal.Instance and Retype.Removed), sorted.
func DepartureMoves(removed []Removal, retyped []Retype) []Move {
	var moved []Move
	for _, r := range removed {
		if r.Instance != "" {
			moved = append(moved, Move{From: r.Instance, To: r.From})
		}
	}
	for _, r := range retyped {
		if r.Removed != RemovedFrom(r.From) {
			moved = append(moved, Move{From: r.From, To: r.Removed})
		}
	}
	SortMoves(moved)
	return slices.Compact(moved)
}

func SortMoves(moved []Move) {
	sort.Slice(moved, func(i, j int) bool {
		if moved[i].To != moved[j].To {
			return moved[i].To < moved[j].To
		}
		return moved[i].From < moved[j].From
	})
}

// WriteMoves writes moved into dir's moves.tf, with the moves of what
// the removed blocks of removed and retyped take alone (see
// DepartureMoves), sorted by where they move to, then those removed
// blocks, sorted by what they remove, each with destroy = false; it
// removes the file if there are none.
func WriteMoves(dir string, moved []Move, removed []Removal, retyped []Retype) error {
	path := filepath.Join(dir, MovesFileName)
	if len(moved) == 0 && len(removed) == 0 && len(retyped) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	sorted := append(slices.Clone(moved), DepartureMoves(removed, retyped)...)
	SortMoves(sorted)
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
	if _, diags := hclfile.ParseDiagnostics(path, content); diags.HasErrors() {
		return fmt.Errorf("%s: %w", MovesFileName, diags)
	}
	return fsutil.WriteFile(path, content, 0o644)
}

// ReadMoves returns the moved blocks of dir's moves.tf; none if dir has
// no moves.tf.
func ReadMoves(dir string) ([]Move, error) {
	f, err := hclfile.LoadOptional(filepath.Join(dir, MovesFileName))
	if err != nil || f == nil {
		return nil, err
	}
	var moved []Move
	for _, b := range f.Syntax.Blocks {
		from, hasFrom := b.Body.Attributes["from"]
		to, hasTo := b.Body.Attributes["to"]
		if b.Type != "moved" || !hasFrom || !hasTo {
			continue
		}
		moved = append(moved, Move{
			From: strings.TrimSpace(string(from.Expr.Range().SliceBytes(f.Src))),
			To:   strings.TrimSpace(string(to.Expr.Range().SliceBytes(f.Src))),
		})
	}
	return moved, nil
}

// ReadRemoved returns the removed blocks of dir's moves.tf, by what they
// remove; none if dir has no moves.tf.
func ReadRemoved(dir string) ([]Removal, error) {
	f, err := hclfile.LoadOptional(filepath.Join(dir, MovesFileName))
	if err != nil || f == nil {
		return nil, err
	}
	var removed []Removal
	for _, b := range f.Syntax.Blocks {
		from, ok := b.Body.Attributes["from"]
		if b.Type != "removed" || !ok {
			continue
		}
		removed = append(removed, Removal{From: strings.TrimSpace(string(from.Expr.Range().SliceBytes(f.Src)))})
	}
	return removed, nil
}
