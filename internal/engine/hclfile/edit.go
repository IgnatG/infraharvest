// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"bytes"
	"slices"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// Edit replaces the bytes from Start to End of File, relative to
// the root, with Text.
type Edit struct {
	File       string
	Start, End int
	Text       []byte
	// Layout is the address of the resource block to lay out once edited,
	// as for an argument added at the end of it.
	Layout string
}

// AppendEdit is the edit that adds text, whole lines, at the end of f,
// the file name, nil where there is none yet: on a line of its own, and
// after a blank line where blank says so and f has anything to keep
// apart from.
func AppendEdit(f *File, name string, text []byte, blank bool) Edit {
	var src []byte
	if f != nil {
		src = f.Src
	}
	var before []byte
	if n := len(src); n > 0 {
		if src[n-1] != '\n' {
			before = append(before, '\n')
		}
		if blank {
			before = append(before, '\n')
		}
	}
	return Edit{File: name, Start: len(src), End: len(src), Text: slices.Concat(before, text)}
}

// DropBackend returns src, a configuration file, without the backend and
// cloud blocks of its terraform blocks, cut out of it, with their lines
// where nothing else is on them: the rest stays byte for byte, so that the
// format check finds what it would in the root, but where src was
// formatted, which it stays. It returns src as it is where it has none,
// or doesn't parse.
func DropBackend(src []byte) []byte {
	f, err := Parse("", src)
	if err != nil {
		return src
	}
	var cuts [][2]int
	for _, b := range f.Syntax.Blocks {
		if b.Type != "terraform" {
			continue
		}
		for _, inner := range b.Body.Blocks {
			if inner.Type != "backend" && inner.Type != "cloud" {
				continue
			}
			rng := inner.Range()
			start, end := rng.Start.Byte, rng.End.Byte
			if ls, le := LineStart(src, start), LineEnd(src, end); len(bytes.TrimSpace(src[ls:start])) == 0 && len(bytes.TrimSpace(src[end:le])) == 0 {
				start, end = ls, le
			}
			cuts = append(cuts, [2]int{start, end})
		}
	}
	if len(cuts) == 0 {
		return src
	}
	formatted := bytes.Equal(hclwrite.Format(src), src)
	out := slices.Clone(src)
	for i := len(cuts) - 1; i >= 0; i-- {
		out = slices.Concat(out[:cuts[i][0]], out[cuts[i][1]:])
	}
	if formatted {
		return hclwrite.Format(out)
	}
	return out
}
