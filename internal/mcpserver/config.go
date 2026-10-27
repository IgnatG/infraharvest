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
// have, so a file that sets one is refused.
var configSettings = func() []string {
	var names []string
	for _, in := range []reflect.Type{reflect.TypeFor[ImportInput](), reflect.TypeFor[DriftInput]()} {
		names = append(names, flagNames(in)...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}()

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

// configText says what f sets: its settings, the providers' settings, its
// state backend and its environments.
func configText(f *config.File) string {
	var parts []string
	if s := settingsText(f.Settings); s != "" {
		parts = append(parts, "settings "+s)
	}
	for _, provider := range slices.Sorted(maps.Keys(f.Providers)) {
		if s := settingsText(f.Providers[provider]); s != "" {
			parts = append(parts, provider+" settings "+s)
		}
	}
	if b := f.Backend; b != nil {
		switch {
		case b.S3 != nil:
			parts = append(parts, fmt.Sprintf("the state backend s3 bucket %q", b.S3.Bucket))
		case b.AzureRM != nil:
			parts = append(parts, fmt.Sprintf("the state backend azurerm storage account %q, container %q", b.AzureRM.StorageAccountName, b.AzureRM.ContainerName))
		case b.GCS != nil:
			parts = append(parts, fmt.Sprintf("the state backend gcs bucket %q", b.GCS.Bucket))
		}
	}
	if len(f.Environments) > 0 {
		parts = append(parts, "the environments "+strings.Join(slices.Sorted(maps.Keys(f.Environments)), ", "))
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, "; ")
}

// settingsText lists settings as name=value, sorted, each value quoted so
// that none can make the confirmation say something else.
func settingsText(settings map[string]any) string {
	var items []string
	for _, name := range slices.Sorted(maps.Keys(settings)) {
		items = append(items, fmt.Sprintf("%s=%q", name, fmt.Sprint(settings[name])))
	}
	return strings.Join(items, ", ")
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
