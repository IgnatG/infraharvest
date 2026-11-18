// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package catalogue holds what infraharvest knows about each Terraform
// resource type, as data: the ID Terraform imports a resource with, the
// arguments to leave out of generated configuration, the resources the
// default selection skips, the child resources a resource brings with it,
// the data source that reads one, and where its provider's APIs name it.
//
// The catalogue is YAML, one file per provider and service under data/,
// built into the binary. Users can add or override entries with files of
// the same format (see Load).
package catalogue

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

//go:embed data
var builtin embed.FS

// AllTypes is the entry whose omit and state_only apply to every type.
const AllTypes = "*"

// Catalogue is a provider's entries, by Terraform type. Its methods only
// read it and hand out copies, as the built-in catalogues are shared by
// the whole process (see Builtin).
type Catalogue struct {
	// provider is the Terraform provider's local name: aws, google.
	provider string
	entries  map[string]*Entry
	// sources records where each entry came from, for errors.
	sources map[string]string

	// byInventory has the entries with an inventory type, by that type in
	// lower case, in the order of their Terraform types, built on the first
	// FromInventory: a catalogue doesn't change once loaded.
	inventoryOnce sync.Once
	byInventory   map[string][]inventoryEntry
}

// inventoryEntry is an entry FromInventory matches: its Terraform type
// and its inventory name pattern in lower case, "" for any name.
type inventoryEntry struct {
	typ, name string
}

var (
	builtinOnce sync.Once
	builtins    map[string]*Catalogue
	builtinErr  error
)

// Builtin returns the provider's built-in catalogue, empty for a provider
// without one. Every caller gets the same one, loaded once, which nothing
// can change: what its methods return are copies.
func Builtin(provider string) (*Catalogue, error) {
	builtinOnce.Do(func() {
		builtins, builtinErr = loadFS(builtin, "data", true, nil)
	})
	if builtinErr != nil {
		return nil, builtinErr
	}
	if c, ok := builtins[provider]; ok {
		return c, nil
	}
	return &Catalogue{provider: provider, entries: map[string]*Entry{}, sources: map[string]string{}}, nil
}

// Providers names the providers with a built-in catalogue.
func Providers() ([]string, error) {
	if _, err := Builtin(""); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(builtins))
	for name := range builtins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Load returns the provider's built-in catalogue with the entries of the
// .yaml and .yml files in dir on top: an entry for a type the built-in
// catalogue has replaces the fields it sets, so omit: [] clears a list.
// Files for the other providers with a built-in catalogue are left out; a
// file for any other provider, or a dir without catalogue files, is an
// error. An empty dir loads the built-in catalogue alone.
func Load(provider, dir string) (*Catalogue, error) {
	base, err := Builtin(provider)
	if err != nil || dir == "" {
		return base, err
	}
	// Checked once merged: an entry may name a built-in child type.
	known, err := Providers()
	if err != nil {
		return nil, err
	}
	overlays, err := loadFS(os.DirFS(dir), ".", false, known)
	if err != nil {
		return nil, fmt.Errorf("catalogue %s: %w", dir, err)
	}
	if len(overlays) == 0 {
		return nil, fmt.Errorf("catalogue %s: no .yaml or .yml files", dir)
	}
	overlay, ok := overlays[provider]
	if !ok {
		return base, nil
	}
	merged := base.clone()
	var errs []error
	for typ, e := range overlay.entries {
		merged.sources[typ] = filepath.Join(dir, overlay.sources[typ])
		for _, err := range checkBuiltinOnly(e) {
			errs = append(errs, fmt.Errorf("%s: %s: %w", merged.sources[typ], typ, err))
		}
		if existing, ok := merged.entries[typ]; ok {
			for _, err := range checkRemove(e) {
				errs = append(errs, fmt.Errorf("%s: %s: %w", merged.sources[typ], typ, err))
			}
			e = existing.merge(e)
		}
		merged.entries[typ] = e
	}
	if err := errors.Join(append(errs, merged.check())...); err != nil {
		return nil, fmt.Errorf("catalogue %s: %w", dir, err)
	}
	return merged, nil
}

// loadFS reads every .yaml or .yml file under root, by provider, and
// checks the catalogues if check is set. With providers, a file for
// another provider is an error: its provider is most likely mistyped.
func loadFS(fsys fs.FS, root string, check bool, providers []string) (map[string]*Catalogue, error) {
	catalogues := map[string]*Catalogue{}
	var errs []error
	err := fs.WalkDir(fsys, root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ext := path.Ext(name); d.IsDir() || (ext != ".yaml" && ext != ".yml") {
			return nil
		}
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		f, err := Parse(content)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			return nil
		}
		if providers != nil && !slices.Contains(providers, f.Provider) {
			errs = append(errs, fmt.Errorf("%s: unknown provider %q: use %s", name, f.Provider, strings.Join(providers, ", ")))
			return nil
		}
		c, ok := catalogues[f.Provider]
		if !ok {
			c = &Catalogue{provider: f.Provider, entries: map[string]*Entry{}, sources: map[string]string{}}
			catalogues[f.Provider] = c
		}
		for typ, e := range f.Types {
			if other, ok := c.sources[typ]; ok {
				errs = append(errs, fmt.Errorf("%s: %s is in %s too", name, typ, other))
				continue
			}
			if e == nil {
				e = &Entry{}
			}
			if e.Service == "" && typ != AllTypes {
				e.Service = f.Service
			}
			c.entries[typ] = e
			c.sources[typ] = name
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if check {
		for _, c := range catalogues {
			if err := c.check(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return catalogues, errors.Join(errs...)
}

// Parse reads one catalogue file, rejecting fields it doesn't know.
func Parse(content []byte) (*File, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	f := &File{}
	if err := decoder.Decode(f); err != nil {
		return nil, err
	}
	if f.Provider == "" {
		return nil, errors.New("provider: missing")
	}
	return f, nil
}

// Provider returns the Terraform provider's local name: aws, google.
func (c *Catalogue) Provider() string {
	return c.provider
}

// Types returns the catalogue's types, sorted, without AllTypes.
func (c *Catalogue) Types() []string {
	types := make([]string, 0, len(c.entries))
	for typ := range c.entries {
		if typ != AllTypes {
			types = append(types, typ)
		}
	}
	sort.Strings(types)
	return types
}

// Entry returns a copy of the entry for a Terraform type: changing it
// changes nothing in c.
func (c *Catalogue) Entry(typ string) (*Entry, bool) {
	e, ok := c.entries[typ]
	if !ok {
		return nil, false
	}
	return e.copy(), true
}

// View returns the entry for a Terraform type itself, not a copy, for
// the hot paths that only read it, such as one lookup per resource: the
// caller must not change it, or anything it holds. Entry returns a copy.
func (c *Catalogue) View(typ string) (*Entry, bool) {
	e, ok := c.entries[typ]
	return e, ok
}

// Source names the file the entry for typ came from.
func (c *Catalogue) Source(typ string) string {
	return c.sources[typ]
}

// MustBuiltin returns the provider's built-in catalogue, and panics if it
// doesn't load: it is part of the binary, and its tests load it.
func MustBuiltin(provider string) *Catalogue {
	c, err := Builtin(provider)
	if err != nil {
		panic(err)
	}
	return c
}
