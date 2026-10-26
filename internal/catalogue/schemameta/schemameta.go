// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package schemameta holds what each provider's own code says about its
// resource types' arguments and `terraform providers schema -json` leaves
// out: the value an argument defaults to, and which arguments exclude or
// need each other (ExactlyOneOf, ConflictsWith, AtLeastOneOf,
// RequiredWith). The engine uses it to leave out defaults and to resolve
// conflicting arguments without a plan or a validation to tell it, which
// the plan it verifies with still checks.
//
// The files, one per provider (aws.json, azurerm.json, google.json), are
// generated from the provider's code at one release (see gen/main.go and
// the schema-metadata task of the remote workflow) and built into the
// binary. They hold only the arguments that have metadata. A type a file
// doesn't list has none the generator could read, and a type marked
// Framework (a Plugin Framework resource) has only its defaults: its
// constraints are validators the provider doesn't expose.
package schemameta

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"
)

//go:embed *.json
var builtin embed.FS

// File is one provider's metadata.
type File struct {
	// Provider is the provider's source address, such as
	// registry.terraform.io/hashicorp/aws.
	Provider string `json:"provider"`
	// Version is the provider release the file was generated from, such as
	// 6.70.0: another release's arguments may differ.
	Version string `json:"version"`
	// Resources are the resource types with metadata, by type.
	Resources map[string]*Resource `json:"resources"`
}

// Resource is one resource type's arguments with metadata, by their path
// through nested blocks without indexes, such as bgp.asn.
type Resource struct {
	// Framework says the type is a Plugin Framework resource: Arguments
	// hold only defaults, and nothing is known of its constraints.
	Framework bool                 `json:"framework,omitempty"`
	Arguments map[string]*Argument `json:"arguments,omitempty"`
}

// Argument is what the provider's code says about one argument. The
// constraints name other arguments by their paths, as Arguments keys
// them.
type Argument struct {
	// Default is the value the provider gives the argument when the
	// configuration leaves it out: a bool, a json.Number or a string; nil
	// if it has none.
	Default any `json:"default,omitempty"`
	// Suppress says the provider lets some other values plan like the
	// default, such as another case of it: only a value equal to the
	// default proves anything.
	Suppress      bool     `json:"suppress,omitempty"`
	ExactlyOneOf  []string `json:"exactly_one_of,omitempty"`
	ConflictsWith []string `json:"conflicts_with,omitempty"`
	AtLeastOneOf  []string `json:"at_least_one_of,omitempty"`
	RequiredWith  []string `json:"required_with,omitempty"`
}

// Argument returns the metadata of the argument at path of typ, or nil.
func (f *File) Argument(typ, path string) *Argument {
	if f == nil {
		return nil
	}
	r, ok := f.Resources[typ]
	if !ok {
		return nil
	}
	return r.Arguments[path]
}

// Resource returns typ's metadata, or nil.
func (f *File) Resource(typ string) *Resource {
	if f == nil {
		return nil
	}
	return f.Resources[typ]
}

var (
	builtinOnce sync.Once
	builtins    map[string]*File
	builtinErr  error
)

// Builtin returns the built-in metadata of provider, by its local name
// (aws, azurerm, google), or nil if there is none. The files are parsed
// once, the first time any is asked for.
func Builtin(provider string) (*File, error) {
	builtinOnce.Do(func() {
		builtins, builtinErr = load(builtin)
	})
	if builtinErr != nil {
		return nil, builtinErr
	}
	return builtins[provider], nil
}

// load parses every .json file of fsys, by name without the extension.
func load(fsys fs.FS) (map[string]*File, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	files := map[string]*File{}
	var errs []error
	for _, name := range names {
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		f, err := Parse(content)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		files[name[:len(name)-len(".json")]] = f
	}
	return files, errors.Join(errs...)
}

// Parse reads one provider's file, rejecting fields it doesn't know.
// Numbers stay json.Number, so that a default keeps its exact value.
func Parse(content []byte) (*File, error) {
	d := json.NewDecoder(bytes.NewReader(content))
	d.UseNumber()
	d.DisallowUnknownFields()
	var f File
	if err := d.Decode(&f); err != nil {
		return nil, err
	}
	if f.Provider == "" || f.Version == "" {
		return nil, errors.New("no provider or version")
	}
	for typ, r := range f.Resources {
		if r == nil {
			return nil, fmt.Errorf("%s: null", typ)
		}
		for path, a := range r.Arguments {
			if a == nil {
				return nil, fmt.Errorf("%s: %s: null", typ, path)
			}
			switch a.Default.(type) {
			case nil, bool, string, json.Number:
			default:
				return nil, fmt.Errorf("%s: %s: default %v isn't a bool, a number or a string", typ, path, a.Default)
			}
		}
	}
	return &f, nil
}
