// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// InputPath is a path into a module input, as Adapter.Inverse names it,
// such as tags, or policies[each.key].
type InputPath struct {
	Variable string
	Steps    []InputStep
}

// InputStep is a step of an InputPath: a key of a map or an attribute of
// an object, an index of a list, or the instance key of the resource the
// module makes with for_each ([each.key]).
type InputStep struct {
	Key     string
	Index   int
	IsIndex bool
	EachKey bool
}

// ParseInputPath parses an input path: a variable's name, then steps
// such as .name, ["key"], [0] or [each.key].
func ParseInputPath(path string) (InputPath, error) {
	end := strings.IndexAny(path, ".[")
	if end < 0 {
		end = len(path)
	}
	in := InputPath{Variable: path[:end]}
	if !hclsyntax.ValidIdentifier(in.Variable) {
		return InputPath{}, fmt.Errorf("%q doesn't start with a variable's name", path)
	}
	rest := path[end:]
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			if !hclsyntax.ValidIdentifier(rest[:end]) {
				return InputPath{}, fmt.Errorf("%q has a step that isn't a name", path)
			}
			in.Steps = append(in.Steps, InputStep{Key: rest[:end]})
			rest = rest[end:]
		case '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return InputPath{}, fmt.Errorf("%q has a [ without its ]", path)
			}
			inner := rest[1:end]
			rest = rest[end+1:]
			switch {
			case inner == "each.key":
				in.Steps = append(in.Steps, InputStep{EachKey: true})
			case strings.HasPrefix(inner, `"`):
				key, err := strconv.Unquote(inner)
				if err != nil {
					return InputPath{}, fmt.Errorf("%q has a key that isn't a quoted string: %s", path, inner)
				}
				in.Steps = append(in.Steps, InputStep{Key: key})
			default:
				i, err := strconv.Atoi(inner)
				if err != nil || i < 0 {
					return InputPath{}, fmt.Errorf("%q has an index that isn't a number, a quoted key or each.key: %s", path, inner)
				}
				in.Steps = append(in.Steps, InputStep{Index: i, IsIndex: true})
			}
		default:
			return InputPath{}, fmt.Errorf("%q has a step that starts with neither . nor [", path)
		}
	}
	return in, nil
}

// InverseTarget splits a key of Inverse into the target it is an
// attribute of, one of Targets, and the attribute's path in it, such as
// aws_iam_role.this and tags; false if none of Targets holds it.
func (a *Adapter) InverseTarget(key string) (target, attribute string, ok bool) {
	for _, t := range a.Targets {
		if len(t) > len(target) && strings.HasPrefix(key, t+".") && len(key) > len(t)+1 {
			target, attribute, ok = t, key[len(t)+1:], true
		}
	}
	return target, attribute, ok
}
