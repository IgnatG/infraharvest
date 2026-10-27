// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/config"
)

// configSettings are the settings a configuration file may set through
// the tools: those whose flags the tools' own parameters set, from their
// flag tags. The others, such as terraform-path, which names a binary to
// run, catalogue and modules, which name directories, or managed-state,
// which names files, would take an agent past the checks the parameters
// have, so a file that sets one is refused. So are modeSettings.
var configSettings = func() []string {
	var names []string
	for _, in := range []reflect.Type{reflect.TypeFor[ImportInput](), reflect.TypeFor[DriftInput]()} {
		names = append(names, flagNames(in)...)
	}
	names = slices.DeleteFunc(names, func(name string) bool { return slices.Contains(modeSettings, name) })
	slices.Sort(names)
	return slices.Compact(names)
}()

// modeSettings are the settings that change how a tool runs, which the
// confirmation the user is shown describes from the tool's own inputs:
// only those set them, so a configuration file can't make the run differ
// from what the user confirmed.
var modeSettings = []string{"incremental", "regenerate", "force-unlock"}

// flagNames returns the flags the flag tags of in, a tool's input, name,
// with those of the structs it embeds.
func flagNames(in reflect.Type) []string {
	var names []string
	for i := range in.NumField() {
		field := in.Field(i)
		if field.Anonymous {
			names = append(names, flagNames(field.Type)...)
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
	// applies says what the file sets, for the user to confirm.
	applies string
}

// loadConfig reads and checks the configuration file at *path, if set,
// which has to be under the root, and sets *path to the path to use. It
// returns nil without one. The caller removes the copy it returns.
func (t *tools) loadConfig(path *string) (*toolConfig, error) {
	if *path == "" {
		return nil, nil
	}
	within, err := t.within("config", *path)
	if err != nil {
		return nil, err
	}
	*path = within
	content, err := os.ReadFile(within)
	if err != nil {
		return nil, err
	}
	copied, err := newSnapshot("infraharvest.yaml", content)
	if err != nil {
		return nil, err
	}
	f, err := config.Load(copied.path, providers.Profiles())
	if err == nil {
		err = checkConfig(f)
	}
	if err != nil {
		copied.remove()
		return nil, fmt.Errorf("config %s: %w", within, err)
	}
	return &toolConfig{path: within, content: content, copied: copied, applies: configText(f)}, nil
}

// checkConfig refuses a file that sets anything but configSettings, the
// state backend and the environments, none of which names a file or a
// binary.
func checkConfig(f *config.File) error {
	if len(f.Modules) > 0 {
		return errors.New("modules: the tools take no modules of the organisation's own; run the command line for them")
	}
	sections := map[string]map[string]any{"settings": f.Settings}
	for provider, settings := range f.Providers {
		sections["providers: "+provider] = settings
	}
	var errs []error
	for _, section := range slices.Sorted(maps.Keys(sections)) {
		for _, name := range slices.Sorted(maps.Keys(sections[section])) {
			if !slices.Contains(configSettings, name) {
				errs = append(errs, fmt.Errorf("%s: %s isn't a setting the tools take, which are %s; run the command line for it", section, name, strings.Join(configSettings, ", ")))
			}
		}
	}
	return errors.Join(errs...)
}

// configText says all that f sets, for the user to confirm: its
// settings, the providers' settings, every field of its state backend and
// of its environments. checkConfig has refused its modules.
func configText(f *config.File) string {
	var parts []string
	for _, part := range []struct {
		name  string
		value any
	}{{"settings", f.Settings}, {"providers", f.Providers}, {"backend", f.Backend}, {"environments", f.Environments}} {
		if text := valueText(reflect.ValueOf(part.value)); text != "" {
			parts = append(parts, part.name+" "+text)
		}
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, "; ")
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
