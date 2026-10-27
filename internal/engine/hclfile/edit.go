// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import "slices"

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
