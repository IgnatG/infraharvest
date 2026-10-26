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
// The files, one per provider (aws.json.gz, azurerm.json.gz,
// google.json.gz), are generated from the provider's code at one release
// (see gen/main.go and the schema-metadata workflows) and built into the
// binary gzipped, each decompressed the first time it is asked for. They
// hold only the arguments that have metadata. A type a file doesn't list
// has none the generator could read, and a type marked Framework (a Plugin
// Framework resource) has only its defaults: its constraints are
// validators the provider doesn't expose.
//
// The generated files are the providers' facts, and MPL-2.0 as the
// providers are (see REUSE.toml). overrides.json is infraharvest's own:
// corrections and additions merged over them (see Overrides).
package schemameta

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
)

//go:embed *.json.gz overrides.json
var builtin embed.FS

// overridesFile is the name of the overrides among the embedded files.
const overridesFile = "overrides.json"

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

// Overrides are corrections and additions to the generated metadata, by
// provider and then resource type, merged over it when it loads: for what
// the generator can't read, such as a Plugin Framework resource's
// constraints, or reads wrong. In a type, each argument replaces the
// generated one whole, and null removes it; a type that is null is
// removed, so that the engine falls back to plans for it.
type Overrides map[string]map[string]*ResourceOverride

// ResourceOverride is one type's overrides.
type ResourceOverride struct {
	// Framework, if set, replaces whether the type is a Plugin Framework
	// resource, whose metadata is only its defaults.
	Framework *bool                `json:"framework,omitempty"`
	Arguments map[string]*Argument `json:"arguments,omitempty"`
}

// loaded is one provider's built-in metadata, decompressed and parsed once.
type loaded struct {
	once sync.Once
	file *File
	err  error
}

var (
	builtins sync.Map // provider -> *loaded

	overridesOnce sync.Once
	overrides     Overrides
	overridesErr  error
)

// Builtin returns the built-in metadata of provider, by its local name
// (aws, azurerm, google), with the overrides merged in, or nil if there is
// none. A provider's file is decompressed and parsed the first time it is
// asked for, and only that one.
func Builtin(provider string) (*File, error) {
	v, _ := builtins.LoadOrStore(provider, &loaded{})
	l := v.(*loaded)
	l.once.Do(func() {
		overridesOnce.Do(func() {
			overrides, overridesErr = readOverrides(builtin, overridesFile)
		})
		if overridesErr != nil {
			l.err = fmt.Errorf("%s: %w", overridesFile, overridesErr)
			return
		}
		l.file, l.err = load(builtin, provider, overrides[provider])
	})
	return l.file, l.err
}

// load reads provider's gzipped file from fsys, or nil if there is none,
// and merges o over it.
func load(fsys fs.FS, provider string, o map[string]*ResourceOverride) (*File, error) {
	name := provider + ".json.gz"
	content, err := fs.ReadFile(fsys, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	f, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	f.merge(o)
	return f, nil
}

// merge applies o, one provider's overrides, to f (see Overrides).
func (f *File) merge(o map[string]*ResourceOverride) {
	for typ, ro := range o {
		if ro == nil {
			delete(f.Resources, typ)
			continue
		}
		r := f.Resources[typ]
		if r == nil {
			r = &Resource{}
			f.Resources[typ] = r
		}
		if ro.Framework != nil {
			r.Framework = *ro.Framework
		}
		for path, a := range ro.Arguments {
			if a == nil {
				delete(r.Arguments, path)
				continue
			}
			if r.Arguments == nil {
				r.Arguments = map[string]*Argument{}
			}
			r.Arguments[path] = a
		}
	}
}

// readOverrides parses the overrides at name in fsys, rejecting fields it
// doesn't know and defaults that aren't a bool, a number or a string.
func readOverrides(fsys fs.FS, name string) (Overrides, error) {
	content, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(content))
	d.UseNumber()
	d.DisallowUnknownFields()
	var o Overrides
	if err := d.Decode(&o); err != nil {
		return nil, err
	}
	for provider, types := range o {
		for typ, ro := range types {
			if ro == nil {
				continue
			}
			for path, a := range ro.Arguments {
				if err := checkDefault(a); err != nil {
					return nil, fmt.Errorf("%s: %s: %s: %w", provider, typ, path, err)
				}
			}
		}
	}
	return o, nil
}

// checkDefault fails if a's default, where a is set, isn't a bool, a
// json.Number or a string.
func checkDefault(a *Argument) error {
	if a == nil {
		return nil
	}
	switch a.Default.(type) {
	case nil, bool, string, json.Number:
		return nil
	}
	return fmt.Errorf("default %v isn't a bool, a number or a string", a.Default)
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
	if f.Resources == nil {
		f.Resources = map[string]*Resource{}
	}
	for typ, r := range f.Resources {
		if r == nil {
			return nil, fmt.Errorf("%s: null", typ)
		}
		for path, a := range r.Arguments {
			if a == nil {
				return nil, fmt.Errorf("%s: %s: null", typ, path)
			}
			if err := checkDefault(a); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", typ, path, err)
			}
		}
	}
	return &f, nil
}
