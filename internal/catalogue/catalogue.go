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
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/go-version"
	"go.yaml.in/yaml/v3"
)

//go:embed data
var builtin embed.FS

// AllTypes is the entry whose omit and state_only apply to every type.
const AllTypes = "*"

// File is one catalogue file: entries for some of a provider's types.
type File struct {
	// Provider is the Terraform provider's local name: aws, google.
	Provider string `yaml:"provider"`
	// Service is the infraharvest service that lists the file's types,
	// unless an entry names another.
	Service string            `yaml:"service,omitempty"`
	Types   map[string]*Entry `yaml:"types"`
}

// Entry is what the catalogue knows about one Terraform resource type.
// Every field is optional; an empty entry changes nothing.
type Entry struct {
	// Service is the infraharvest service that lists the type.
	Service string `yaml:"service,omitempty"`
	// ImportID builds the ID Terraform imports a resource with from what
	// its lister recorded: the first template whose fields all have
	// values (see Template). Without it, the lister's ID is the import ID.
	ImportID Templates `yaml:"import_id,omitempty"`
	// NotImportable says why Terraform can't import the type: the import
	// reports its resources as not importable instead of trying.
	NotImportable string `yaml:"not_importable,omitempty"`
	// Omit names arguments to leave out of generated configuration, or,
	// as name=value, attributes to leave out where they are set to value:
	// deprecated ones whose settings separate resources carry, and ones
	// the provider computes.
	Omit []string `yaml:"omit,omitempty"`
	// Defaults names, as path=value, the values the provider or the cloud
	// gives optional arguments it isn't told, such as a bucket's
	// storage_class=STANDARD, by their path in nested blocks
	// (soft_delete_policy.retention_duration_seconds=604800): the engine
	// leaves out the arguments set to them where the plan stays the same.
	Defaults []string `yaml:"defaults,omitempty"`
	// StateOnly names arguments the provider keeps only in state, such as
	// settings it uses when it deletes a resource: an import leaves them
	// unset, so the verification gate accepts a plan that sets them.
	StateOnly []string `yaml:"state_only,omitempty"`
	// ProviderVersions adds to Omit, Defaults and StateOnly, by a
	// constraint on the provider's version (">= 5.0", "< 5.0"), what holds
	// only for the versions it accepts, such as a default a major release
	// changed.
	ProviderVersions map[string]*Versioned `yaml:"provider_versions,omitempty"`
	// KeepEmpty names optional arguments whose empty value ("", [] or {})
	// the provider documents as a setting of its own, such as one that
	// removes what the cloud adds by default: they stay where they are set
	// empty, which the engine otherwise leaves out.
	KeepEmpty []string `yaml:"keep_empty,omitempty"`
	// Skip lists the resources the default selection leaves out, with the
	// reason, such as the ones the cloud creates itself.
	Skip []SkipRule `yaml:"skip,omitempty"`
	// Children are the types of the separate resources that hold parts of
	// a resource's configuration, imported with it when it has them, such
	// as an S3 bucket's versioning. The provider finds out which a
	// resource has.
	Children []string `yaml:"children,omitempty"`
	// DataSource reads one resource of the type by its import ID, so that
	// configuration can refer to a resource the selection leaves out.
	DataSource *DataSource `yaml:"data_source,omitempty"`
	// ARN is where the ARNs of an AWS type whose IDs aren't ARNs name its
	// resources, to find their tags.
	ARN *ARN `yaml:"arn,omitempty"`
	// CloudControl, for an AWS type listed through the Cloud Control API,
	// is its CloudFormation type.
	CloudControl *CloudControl `yaml:"cloud_control,omitempty"`
	// Inventory is how the cloud's inventory API names resources of the
	// type: Cloud Asset Inventory for Google, Resource Graph for Azure.
	Inventory *Inventory `yaml:"inventory,omitempty"`
	// Placeholder is for configuration Terraform can't read back, such as
	// a Lambda function's code.
	Placeholder *Placeholder `yaml:"placeholder,omitempty"`
	// Lifecycle is lifecycle settings every resource of the type gets,
	// such as create_before_destroy for what others refer to while it is
	// replaced.
	Lifecycle *Lifecycle `yaml:"lifecycle,omitempty"`
	// Remove, in an overlay of a built-in entry, clears these of its
	// fields, which an overlay can't clear by setting them: data_source,
	// arn, cloud_control, inventory, placeholder and lifecycle.
	Remove []string `yaml:"remove,omitempty"`
}

// removable are the fields an overlay's remove clears, by name.
var removable = map[string]struct {
	set   func(*Entry) bool
	clear func(*Entry)
}{
	"data_source":   {func(e *Entry) bool { return e.DataSource != nil }, func(e *Entry) { e.DataSource = nil }},
	"arn":           {func(e *Entry) bool { return e.ARN != nil }, func(e *Entry) { e.ARN = nil }},
	"cloud_control": {func(e *Entry) bool { return e.CloudControl != nil }, func(e *Entry) { e.CloudControl = nil }},
	"inventory":     {func(e *Entry) bool { return e.Inventory != nil }, func(e *Entry) { e.Inventory = nil }},
	"placeholder":   {func(e *Entry) bool { return e.Placeholder != nil }, func(e *Entry) { e.Placeholder = nil }},
	"lifecycle":     {func(e *Entry) bool { return e.Lifecycle != nil }, func(e *Entry) { e.Lifecycle = nil }},
}

// Lifecycle is lifecycle settings for every resource of a type, with the
// reason, which goes in a comment above them.
type Lifecycle struct {
	CreateBeforeDestroy bool   `yaml:"create_before_destroy"`
	Reason              string `yaml:"reason"`
}

// Versioned is what an entry adds for the provider versions a constraint
// accepts (see Entry.ProviderVersions), as the entry's fields of the same
// names do for every version.
type Versioned struct {
	Omit      []string `yaml:"omit,omitempty"`
	Defaults  []string `yaml:"defaults,omitempty"`
	StateOnly []string `yaml:"state_only,omitempty"`
}

// Placeholder sets arguments that Terraform can't read back from the cloud,
// such as where a Lambda function's code comes from, to placeholders,
// when the generated configuration sets none of OneOf, and makes Terraform
// ignore changes to Ignore: what is deployed stays as it is.
type Placeholder struct {
	// OneOf are the arguments one of which the provider requires.
	OneOf []string `yaml:"one_of"`
	// Set has, by argument, its placeholder value: {argument} in it is
	// the value of one of the resource's string arguments.
	Set map[string]string `yaml:"set"`
	// Ignore are the arguments Terraform then ignores changes to.
	Ignore []string `yaml:"ignore"`
	// Reason says why, in a comment above lifecycle.ignore_changes.
	Reason string `yaml:"reason"`
}

// SkipRule matches resources by their ID or attributes: all the
// conditions it sets must hold. A rule with all set matches every
// resource of the type.
type SkipRule struct {
	Reason string `yaml:"reason"`
	All    bool   `yaml:"all,omitempty"`
	// IDs matches resources with one of these IDs.
	IDs []string `yaml:"ids,omitempty"`
	// IDPrefixes matches resources whose ID starts with one of these.
	IDPrefixes []string `yaml:"id_prefixes,omitempty"`
	// Attributes matches resources whose attributes have these values.
	Attributes map[string]string `yaml:"attributes,omitempty"`
	// AttributePrefixes matches resources whose attributes start with one
	// of these, such as a name the cloud gives what it creates.
	AttributePrefixes map[string][]string `yaml:"attribute_prefixes,omitempty"`
	// AttributeSuffixes matches resources whose attributes end with one of
	// these, such as the domain of a service account the cloud manages.
	AttributeSuffixes map[string][]string `yaml:"attribute_suffixes,omitempty"`
	// IgnoreCase compares IDs and attributes whatever their case, as Azure
	// names are.
	IgnoreCase bool `yaml:"ignore_case,omitempty"`
}

// Inventory is a resource type of a cloud's inventory API, such as
// compute.googleapis.com/Network or Microsoft.Network/networkWatchers, and,
// when the type covers several Terraform types, the pattern of the names
// of the resources of this one: * stands for one segment of the name,
// between slashes. Both match whatever the case.
type Inventory struct {
	Type string `yaml:"type"`
	Name string `yaml:"name,omitempty"`
}

// DataSource is a data source's type and the argument that takes the
// resource's import ID.
type DataSource struct {
	Type     string `yaml:"type"`
	Argument string `yaml:"argument"`
}

// ARN is an ARN's service and the resource type its resource part starts
// with: "" if the part is the name alone, as for an S3 bucket.
type ARN struct {
	Service      string `yaml:"service"`
	ResourceType string `yaml:"resource_type"`
}

// CloudControl is the CloudFormation type Cloud Control lists a type as,
// and the one property of its primary identifier, whose value Cloud
// Control returns as each resource's identifier and which is also the
// Terraform import ID.
type CloudControl struct {
	Type       string `yaml:"type"`
	Identifier string `yaml:"identifier"`
}

// Catalogue is a provider's entries, by Terraform type.
type Catalogue struct {
	Provider string
	entries  map[string]*Entry
	// sources records where each entry came from, for errors.
	sources map[string]string
}

var (
	builtinOnce sync.Once
	builtins    map[string]*Catalogue
	builtinErr  error
)

// Builtin returns the provider's built-in catalogue, empty for a provider
// without one.
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
	return &Catalogue{Provider: provider, entries: map[string]*Entry{}, sources: map[string]string{}}, nil
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
			c = &Catalogue{Provider: f.Provider, entries: map[string]*Entry{}, sources: map[string]string{}}
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

// Entry returns the entry for a Terraform type.
func (c *Catalogue) Entry(typ string) (*Entry, bool) {
	e, ok := c.entries[typ]
	return e, ok
}

// Source names the file the entry for typ came from.
func (c *Catalogue) Source(typ string) string {
	return c.sources[typ]
}

// Omit returns the arguments to leave out of generated configuration
// whatever the provider's version, per type and under AllTypes for every
// type: the entries whose conditions don't depend on what a lister
// recorded (see ResourceOmit).
func (c *Catalogue) Omit() map[string][]string {
	return c.OmitFor(nil)
}

// OmitFor returns the arguments to leave out of configuration generated
// with version v of the provider: Omit, and what the entries'
// provider_versions add for v. A nil v adds nothing.
func (c *Catalogue) OmitFor(v *version.Version) map[string][]string {
	return c.lists(v, func(e *Entry) []string { return withoutListerConditions(e.Omit) }, func(x *Versioned) []string { return withoutListerConditions(x.Omit) })
}

// withoutListerConditions returns the omit entries without a condition on
// what a lister recorded (see ResourceOmit).
func withoutListerConditions(entries []string) []string {
	return slices.DeleteFunc(slices.Clone(entries), func(entry string) bool {
		_, conditions := listerConditions(entry)
		return len(conditions) > 0
	})
}

// ResourceOmit returns the arguments to leave out of the generated
// configuration of one resource of typ besides those Omit returns: the
// entries with a condition on what its lister recorded, @attribute=value
// (or @attribute, set and not empty, or !@attribute, not), where those
// conditions hold for attributes, without them.
func (c *Catalogue) ResourceOmit(typ string, attributes map[string]string) []string {
	e, ok := c.entries[typ]
	if !ok {
		return nil
	}
	var list []string
	for _, entry := range e.Omit {
		rest, conditions := listerConditions(entry)
		if len(conditions) > 0 && !slices.ContainsFunc(conditions, func(cond string) bool { return !listerConditionHolds(cond, attributes) }) {
			list = append(list, rest)
		}
	}
	return list
}

// listerConditions splits an omit entry into the entry without its
// conditions on what the lister recorded, and those conditions.
func listerConditions(entry string) (string, []string) {
	target, conditions, ok := strings.Cut(entry, " when ")
	if !ok {
		return entry, nil
	}
	var kept, lister []string
	for _, c := range strings.Split(conditions, " and ") {
		c = strings.TrimSpace(c)
		if strings.HasPrefix(strings.TrimPrefix(c, "!"), "@") {
			lister = append(lister, c)
		} else {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return target, lister
	}
	return target + " when " + strings.Join(kept, " and "), lister
}

// listerConditionHolds reports whether a condition on what a lister
// recorded, @name=value, @name or !@name, holds for attributes.
func listerConditionHolds(condition string, attributes map[string]string) bool {
	negate := strings.HasPrefix(condition, "!")
	name, value, valued := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(condition, "!"), "@"), "=")
	got := attributes[name]
	holds := got != ""
	if valued {
		holds = got == value
	}
	return holds != negate
}

// Defaults returns the values optional arguments default to whatever the
// provider's version, as path=value, per type.
func (c *Catalogue) Defaults() map[string][]string {
	return c.DefaultsFor(nil)
}

// DefaultsFor returns the values optional arguments default to with
// version v of the provider: Defaults, and what the entries'
// provider_versions add for v, such as a default a major release changed.
func (c *Catalogue) DefaultsFor(v *version.Version) map[string][]string {
	return c.lists(v, func(e *Entry) []string { return e.Defaults }, func(x *Versioned) []string { return x.Defaults })
}

// StateOnly returns the arguments the provider keeps only in state,
// whatever its version, per type and under AllTypes for every type.
func (c *Catalogue) StateOnly() map[string][]string {
	return c.StateOnlyFor(nil)
}

// StateOnlyFor returns the arguments version v of the provider keeps only
// in state: StateOnly, and what the entries' provider_versions add for v.
func (c *Catalogue) StateOnlyFor(v *version.Version) map[string][]string {
	return c.lists(v, func(e *Entry) []string { return e.StateOnly }, func(x *Versioned) []string { return x.StateOnly })
}

// lists returns each entry's field, with what its provider_versions add
// for v in constraint order, by type.
func (c *Catalogue) lists(v *version.Version, field func(*Entry) []string, versioned func(*Versioned) []string) map[string][]string {
	lists := map[string][]string{}
	for typ, e := range c.entries {
		l := append([]string(nil), field(e)...)
		if v != nil {
			for _, constraint := range slices.Sorted(maps.Keys(e.ProviderVersions)) {
				if accepts(constraint, v) && e.ProviderVersions[constraint] != nil {
					l = append(l, versioned(e.ProviderVersions[constraint])...)
				}
			}
		}
		if len(l) > 0 {
			lists[typ] = l
		}
	}
	return lists
}

// accepts reports whether the version constraint accepts v; check
// rejects constraints that don't parse.
func accepts(constraint string, v *version.Version) bool {
	c, err := version.NewConstraint(constraint)
	return err == nil && c.Check(v)
}

// KeepEmpty returns the arguments that stay where they are set empty,
// per type.
func (c *Catalogue) KeepEmpty() map[string][]string {
	return c.lists(nil, func(e *Entry) []string { return e.KeepEmpty }, func(*Versioned) []string { return nil })
}

// DataSources returns the data source of each type that has one.
func (c *Catalogue) DataSources() map[string]DataSource {
	sources := map[string]DataSource{}
	for typ, e := range c.entries {
		if e.DataSource != nil {
			sources[typ] = *e.DataSource
		}
	}
	return sources
}

// ImportID returns the ID Terraform imports a resource of typ with, from
// the ID its lister recorded and its attributes. It returns false, with
// the reason, when Terraform can't import it: the type isn't importable,
// or no template's fields all have values.
func (c *Catalogue) ImportID(typ, id string, attributes map[string]string) (string, bool, string) {
	e, ok := c.entries[typ]
	if !ok {
		return id, true, ""
	}
	if e.NotImportable != "" {
		return "", false, e.NotImportable
	}
	if len(e.ImportID) == 0 {
		return id, true, ""
	}
	for _, t := range e.ImportID {
		if rendered, ok := t.Render(id, attributes); ok {
			return rendered, true, ""
		}
	}
	return "", false, fmt.Sprintf("missing a field of every import ID template: %s", templateList(e.ImportID))
}

// Skipped returns why the default selection leaves out a resource of typ,
// or "" if it doesn't: the type's rules first, then those for every type.
func (c *Catalogue) Skipped(typ, id string, attributes map[string]string) string {
	for _, t := range []string{typ, AllTypes} {
		e, ok := c.entries[t]
		if !ok {
			continue
		}
		for _, rule := range e.Skip {
			if rule.matches(id, attributes) {
				return rule.Reason
			}
		}
	}
	return ""
}

// InventoryTypes returns the asset types of the cloud's inventory API the
// catalogue maps to Terraform types, sorted.
func (c *Catalogue) InventoryTypes() []string {
	seen := map[string]bool{}
	var types []string
	for _, e := range c.entries {
		if e.Inventory != nil && !seen[e.Inventory.Type] {
			seen[e.Inventory.Type] = true
			types = append(types, e.Inventory.Type)
		}
	}
	sort.Strings(types)
	return types
}

// FromInventory returns the Terraform types of a resource the cloud's
// inventory API lists as assetType, named name: the types whose inventory
// entry has that type and a name pattern that matches, if it has one,
// sorted. More than one means the catalogue can't tell them apart.
func (c *Catalogue) FromInventory(assetType, name string) []string {
	var types []string
	for typ, e := range c.entries {
		if e.Inventory != nil && e.Inventory.matches(assetType, name) {
			types = append(types, typ)
		}
	}
	sort.Strings(types)
	return types
}

func (i *Inventory) matches(assetType, name string) bool {
	if !strings.EqualFold(i.Type, assetType) {
		return false
	}
	if i.Name == "" {
		return true
	}
	ok, _ := path.Match(strings.ToLower(i.Name), strings.ToLower(name))
	return ok
}

func (r SkipRule) matches(id string, attributes map[string]string) bool {
	equal := func(a, b string) bool { return a == b }
	hasPrefix := strings.HasPrefix
	hasSuffix := strings.HasSuffix
	if r.IgnoreCase {
		equal = strings.EqualFold
		hasPrefix = func(s, prefix string) bool {
			return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
		}
		hasSuffix = func(s, suffix string) bool {
			return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
		}
	}
	if len(r.IDs) > 0 && !slices.ContainsFunc(r.IDs, func(want string) bool { return equal(id, want) }) {
		return false
	}
	if len(r.IDPrefixes) > 0 && !slices.ContainsFunc(r.IDPrefixes, func(p string) bool { return hasPrefix(id, p) }) {
		return false
	}
	for k, v := range r.Attributes {
		if !equal(attributes[k], v) {
			return false
		}
	}
	for k, prefixes := range r.AttributePrefixes {
		value, ok := attributes[k]
		if !ok || !slices.ContainsFunc(prefixes, func(p string) bool { return hasPrefix(value, p) }) {
			return false
		}
	}
	for k, suffixes := range r.AttributeSuffixes {
		value, ok := attributes[k]
		if !ok || !slices.ContainsFunc(suffixes, func(s string) bool { return hasSuffix(value, s) }) {
			return false
		}
	}
	return true
}

func (c *Catalogue) clone() *Catalogue {
	clone := &Catalogue{Provider: c.Provider, entries: make(map[string]*Entry, len(c.entries)), sources: make(map[string]string, len(c.sources))}
	for typ, e := range c.entries {
		copied := *e
		clone.entries[typ] = &copied
	}
	for typ, s := range c.sources {
		clone.sources[typ] = s
	}
	return clone
}

// merge returns e with the fields overlay sets replaced.
func (e *Entry) merge(overlay *Entry) *Entry {
	merged := *e
	if overlay.Service != "" {
		merged.Service = overlay.Service
	}
	if overlay.ImportID != nil {
		merged.ImportID = overlay.ImportID
		merged.NotImportable = ""
	}
	if overlay.NotImportable != "" {
		merged.NotImportable = overlay.NotImportable
		merged.ImportID = nil
	}
	if overlay.Omit != nil {
		merged.Omit = overlay.Omit
	}
	if overlay.Defaults != nil {
		merged.Defaults = overlay.Defaults
	}
	if overlay.StateOnly != nil {
		merged.StateOnly = overlay.StateOnly
	}
	if overlay.ProviderVersions != nil {
		merged.ProviderVersions = overlay.ProviderVersions
	}
	if overlay.KeepEmpty != nil {
		merged.KeepEmpty = overlay.KeepEmpty
	}
	if overlay.Skip != nil {
		merged.Skip = overlay.Skip
	}
	if overlay.Children != nil {
		merged.Children = overlay.Children
	}
	if overlay.DataSource != nil {
		merged.DataSource = overlay.DataSource
	}
	if overlay.ARN != nil {
		merged.ARN = overlay.ARN
	}
	if overlay.CloudControl != nil {
		merged.CloudControl = overlay.CloudControl
	}
	if overlay.Inventory != nil {
		merged.Inventory = overlay.Inventory
	}
	if overlay.Placeholder != nil {
		merged.Placeholder = overlay.Placeholder
	}
	if overlay.Lifecycle != nil {
		merged.Lifecycle = overlay.Lifecycle
	}
	for _, name := range overlay.Remove {
		if field, ok := removable[name]; ok {
			field.clear(&merged)
		}
	}
	merged.Remove = nil
	return &merged
}

// checkRemove reports what's wrong with an overlay entry's remove: a name
// that isn't a removable field, one named twice, or one the entry sets
// too.
func checkRemove(overlay *Entry) []error {
	var errs []error
	seen := map[string]bool{}
	for _, name := range overlay.Remove {
		field, ok := removable[name]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("remove: %q: want %s", name, strings.Join(slices.Sorted(maps.Keys(removable)), ", ")))
		case seen[name]:
			errs = append(errs, fmt.Errorf("remove: %s twice", name))
		case field.set(overlay):
			errs = append(errs, fmt.Errorf("remove: %s is set too", name))
		}
		seen[name] = true
	}
	return errs
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

// Placeholders returns the placeholder of each type that has one.
func (c *Catalogue) Placeholders() map[string]Placeholder {
	placeholders := map[string]Placeholder{}
	for typ, e := range c.entries {
		if e.Placeholder != nil {
			placeholders[typ] = *e.Placeholder
		}
	}
	return placeholders
}

// Lifecycles returns the lifecycle settings of each type that has them.
func (c *Catalogue) Lifecycles() map[string]Lifecycle {
	lifecycles := map[string]Lifecycle{}
	for typ, e := range c.entries {
		if e.Lifecycle != nil {
			lifecycles[typ] = *e.Lifecycle
		}
	}
	return lifecycles
}
