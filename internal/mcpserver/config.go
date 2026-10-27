// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/config"
)

// configUse is what a tool takes of a configuration file: the settings
// its command applies from one, less those that would take an agent past
// the checks the tools' parameters have, such as terraform-path, which
// names a binary to run, catalogue and modules, which name directories,
// or managed-state, which names files, and less modeSettings. A file that
// sets anything else is refused, so that what the user confirms is what
// applies, and nothing the run ignores.
type configUse struct {
	// tool names the tool, for the errors.
	tool string
	// provider is the provider command a discover or an import runs,
	// which applies the file's settings, then its section of providers,
	// with the backend and the environments; "" for drift, which applies
	// only driftConfigSettings of the file's settings.
	provider string
}

// driftConfigSettings are the settings drift's --config applies, engine,
// terraform-path, engine-timeout and catalogue (see
// config.File.ApplyShared), less
// terraform-path, a binary to run, and catalogue, a directory, which the
// drift tools take as a parameter, checked under the root.
var driftConfigSettings = []string{"engine", "engine-timeout"}

// settings returns the settings the tool takes under section, settings
// for "" or a provider's section of providers: for discover and import,
// those whose flags the import tool's parameters set for that provider,
// as its command applies a file's settings and its section alike; for
// drift, driftConfigSettings under settings alone, as it applies no
// provider's section.
func (u configUse) settings(section string) []string {
	if u.provider == "" {
		if section == "" {
			return driftConfigSettings
		}
		return nil
	}
	if section == "" {
		section = u.provider
	}
	names := slices.DeleteFunc(flagNames(reflect.TypeFor[ImportInput](), section), func(name string) bool { return slices.Contains(modeSettings, name) })
	slices.Sort(names)
	return names
}

// modeSettings are the settings that change how a tool runs, which the
// confirmation the user is shown describes from the tool's own inputs:
// only those set them, so a configuration file can't make the run differ
// from what the user confirmed.
var modeSettings = []string{"incremental", "regenerate", "force-unlock"}

// flagNames returns the flags the flag tags of in, a tool's input, name,
// with those of the structs it embeds, for provider: a field whose
// providers tag names others is left out, as provider's command has no
// such flag, or one with another meaning.
func flagNames(in reflect.Type, provider string) []string {
	var names []string
	for i := range in.NumField() {
		field := in.Field(i)
		if field.Anonymous {
			names = append(names, flagNames(field.Type, provider)...)
			continue
		}
		if takers := scopeProviders(field); len(takers) > 0 && !slices.Contains(takers, provider) {
			continue
		}
		if tag, ok := field.Tag.Lookup("flag"); ok {
			name, _, _ := strings.Cut(tag, ",")
			names = append(names, name)
		}
	}
	return names
}

// toolConfig is a configuration file a tool runs with: read once, checked,
// and run from a private copy of what was checked, so that a file the
// agent changes in between, such as after the user confirmed it, isn't
// what runs.
type toolConfig struct {
	// path is the file as the agent named it, under the root.
	path    string
	content []byte
	copied  *snapshot
	file    *config.File
	use     configUse
	// applies says what the file sets, for the user to confirm.
	applies string
}

// loadConfig reads and checks the configuration file at *path, if set,
// which has to be under the root, for use, and sets *path to the path to
// use. It returns nil without one. The caller removes the copy it
// returns.
func (t *tools) loadConfig(path *string, use configUse) (*toolConfig, error) {
	if *path == "" {
		return nil, nil
	}
	within, content, err := t.readWithin("config", *path)
	if err != nil {
		return nil, err
	}
	*path = within
	copied, err := newSnapshot("infraharvest.yaml", content)
	if err != nil {
		return nil, err
	}
	f, err := config.Load(copied.path, providers.Profiles())
	if err == nil {
		err = checkConfig(f, use)
	}
	if err != nil {
		copied.remove()
		return nil, fmt.Errorf("config %s: %w", within, err)
	}
	return &toolConfig{path: within, content: content, copied: copied, file: f, use: use, applies: configText(f, use)}, nil
}

// checkConfig refuses a file that sets anything but what use takes: the
// settings it takes under settings and each provider's section, and, for
// discover and import, the state backend and the environments, none of
// which names a file or a binary.
func checkConfig(f *config.File, use configUse) error {
	if len(f.Modules) > 0 {
		return errors.New("modules: the tools take no modules of the organisation's own; run the command line for them")
	}
	var errs []error
	if use.provider == "" {
		if f.Backend != nil {
			errs = append(errs, fmt.Errorf("backend: the %s tool doesn't apply it; pass a file with engine or engine-timeout under settings alone", use.tool))
		}
		if len(f.Environments) > 0 {
			errs = append(errs, fmt.Errorf("environments: the %s tool doesn't apply them; pass a file with engine or engine-timeout under settings alone, and the environments parameter", use.tool))
		}
	}
	sections := map[string]string{"settings": ""}
	for provider := range f.Providers {
		sections["providers: "+provider] = provider
	}
	for _, section := range slices.Sorted(maps.Keys(sections)) {
		settings := f.Settings
		if provider := sections[section]; provider != "" {
			settings = f.Providers[provider]
		}
		takes := use.settings(sections[section])
		for _, name := range slices.Sorted(maps.Keys(settings)) {
			if slices.Contains(takes, name) {
				continue
			}
			which := "it takes none there"
			if len(takes) > 0 {
				which = "which are " + strings.Join(takes, ", ")
			}
			errs = append(errs, fmt.Errorf("%s: %s isn't a setting the %s tool takes, %s; run the command line for it", section, name, use.tool, which))
		}
	}
	return errors.Join(errs...)
}

// configText says all of f that applies to use's run, for the user to
// confirm: its settings, use's provider's section, and every field of its
// state backend and of its environments. checkConfig has refused the
// rest: its modules, and for drift, all but settings.
func configText(f *config.File, use configUse) string {
	var section map[string]map[string]any
	if settings, ok := f.Providers[use.provider]; ok && use.provider != "" {
		section = map[string]map[string]any{use.provider: settings}
	}
	var parts []string
	for _, part := range []struct {
		name  string
		value any
	}{{"settings", f.Settings}, {"providers", section}, {"backend", f.Backend}, {"environments", f.Environments}} {
		if text := valueText(reflect.ValueOf(part.value)); text != "" {
			parts = append(parts, part.name+" "+text)
		}
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, "; ")
}

// scope returns s, the scope a tool's input sets, with what the file sets
// of the rest, as the provider command applies it (see config.File.Apply):
// a flag the input sets wins, then the file's settings, then the
// provider's section. Its values are checked as the input's are, as the
// confirmation names them.
func (c *toolConfig) scope(s Scope) (Scope, error) {
	if c == nil {
		return s, nil
	}
	v := reflect.ValueOf(&s).Elem()
	for i := range v.NumField() {
		field, value := v.Type().Field(i), v.Field(i)
		tag, ok := field.Tag.Lookup("flag")
		if !ok || isSet(value) {
			continue
		}
		name, mode, _ := strings.Cut(tag, ",")
		for _, settings := range []map[string]any{c.file.Settings, c.file.Providers[c.use.provider]} {
			setting, ok := settings[name]
			if !ok {
				continue
			}
			if err := setScopeField(value, setting, mode == "each"); err != nil {
				return s, fmt.Errorf("config %s: %s: %w", c.path, name, err)
			}
			break
		}
	}
	if err := checkScopeNames(s); err != nil {
		return s, fmt.Errorf("config %s: %w", c.path, err)
	}
	if _, err := flagArgs(s); err != nil {
		return s, fmt.Errorf("config %s: %w", c.path, err)
	}
	return s, nil
}

// setScopeField sets value, a field of Scope, to setting, a value of the
// configuration file, as the flag takes it (see config.File.Apply): a
// list's values split at commas, unless each, a flag given once per
// value, takes each whole.
func setScopeField(value reflect.Value, setting any, each bool) error {
	items, ok := setting.([]any)
	if !ok {
		items = []any{setting}
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		switch item.(type) {
		case string, bool, int, float64:
			values = append(values, fmt.Sprint(item))
		default:
			return fmt.Errorf("unsupported value %v", item)
		}
	}
	switch value.Kind() {
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.Join(values, ","))
		if err != nil {
			return err
		}
		value.SetBool(b)
	case reflect.String:
		value.SetString(strings.Join(values, ","))
	case reflect.Slice:
		if !each {
			values = strings.Split(strings.Join(values, ","), ",")
		}
		value.Set(reflect.ValueOf(values))
	}
	return nil
}

// valueText writes out v, a value of the configuration file, whole: a
// struct's fields that are set, by their YAML names, and a map's entries,
// sorted, each as {name: value, ...}, a list as [value, ...], and every
// text quoted, keys too, so that none can make the confirmation say
// something else. It returns "" for a value that isn't set.
func valueText(v reflect.Value) string {
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Invalid:
		return ""
	case reflect.String:
		return fmt.Sprintf("%q", v.String())
	case reflect.Slice:
		items := make([]string, v.Len())
		for i := range v.Len() {
			items[i] = valueText(v.Index(i))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case reflect.Map:
		if v.Len() == 0 {
			return ""
		}
		var entries []string
		for _, key := range slices.SortedFunc(slices.Values(v.MapKeys()), func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) }) {
			entries = append(entries, fmt.Sprintf("%q: %s", key.String(), valueText(v.MapIndex(key))))
		}
		return "{" + strings.Join(entries, ", ") + "}"
	case reflect.Struct:
		var fields []string
		for i := range v.NumField() {
			name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("yaml"), ",")
			if v.Field(i).IsZero() || name == "" || name == "-" {
				continue
			}
			if text := valueText(v.Field(i)); text != "" {
				fields = append(fields, name+": "+text)
			}
		}
		if len(fields) == 0 {
			return ""
		}
		return "{" + strings.Join(fields, ", ") + "}"
	}
	return fmt.Sprint(v.Interface())
}

// message says, for the user to confirm, that the file applies, and what
// it sets.
func (c *toolConfig) message() string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf(" The configuration file %q applies, setting %s.", c.path, c.applies)
}

// sum is the content the user confirms, for confirmationState.
func (c *toolConfig) sum() []byte {
	if c == nil {
		return nil
	}
	return c.content
}

// flag is the --config argument as the user sees it, naming the file.
func (c *toolConfig) flag() []string {
	if c == nil {
		return nil
	}
	return []string{"--config=" + c.path}
}

// run returns args, which have flag in them, as they run: with the
// private copy instead of the file.
func (c *toolConfig) run(args []string) []string {
	if c == nil {
		return args
	}
	run := slices.Clone(args)
	for i, a := range run {
		if a == "--config="+c.path {
			run[i] = "--config=" + c.copied.path
		}
	}
	return run
}

func (c *toolConfig) remove() {
	if c != nil {
		c.copied.remove()
	}
}
