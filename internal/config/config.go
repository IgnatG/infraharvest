// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package config reads infraharvest's configuration file. Every setting in
// it is a command-line flag, which wins when given; the file adds the state
// backend the generated roots use.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/spf13/pflag"
	"github.com/zclconf/go-cty/cty"
	"go.yaml.in/yaml/v3"
)

// Version is the configuration file format's version.
const Version = 1

// ProviderCommands are the provider commands providers: holds settings
// for, whichever of them the binary is built with.
var ProviderCommands = []string{"aws", "azure", "google"}

// File is a configuration file.
type File struct {
	Version int `yaml:"version"`
	// Settings for every provider, by flag name: engine, terraform-path,
	// out, layout, selection, all, allow-partial, ...
	Settings map[string]any `yaml:"settings,omitempty"`
	// Providers holds settings for one provider command, by flag name:
	// aws: { profile: prod, regions: [eu-west-2], services: [vpc, s3] }.
	Providers map[string]map[string]any `yaml:"providers,omitempty"`
	// Backend is the state backend of every generated root.
	Backend *Backend `yaml:"backend,omitempty"`
	// Modules are the organisation's own modules, which clusters of
	// resources move into before the curated public ones (see
	// adapters.Generic).
	Modules []Module `yaml:"modules,omitempty"`
	// Environments are the estate's environments: each gets roots of its
	// own, under {out}/envs/<name>/ (see Environments.For).
	Environments Environments `yaml:"environments,omitempty"`
}

// Module is a module of the organisation's own, at a pinned version: a git
// repository at a tag or commit (git::https://host/repo.git//dir?ref=v1.2.0),
// a private registry's module at an exact version
// (<host>/<namespace>/<name>/<provider> and version), or a directory,
// relative to the configuration file.
type Module struct {
	Source  string `yaml:"source"`
	Version string `yaml:"version,omitempty"`
}

// Backend is one state backend. Each root gets its own state key, made of
// the prefix and the root's path in the output directory.
type Backend struct {
	S3      *S3      `yaml:"s3,omitempty"`
	AzureRM *AzureRM `yaml:"azurerm,omitempty"`
	GCS     *GCS     `yaml:"gcs,omitempty"`
}

// S3 stores state in an S3 bucket, locked with S3's native lock file
// (use_lockfile), never DynamoDB.
type S3 struct {
	Bucket    string `yaml:"bucket"`
	Region    string `yaml:"region"`
	KeyPrefix string `yaml:"key_prefix,omitempty"`
	KMSKeyID  string `yaml:"kms_key_id,omitempty"`
}

// AzureRM stores state in an Azure storage container.
type AzureRM struct {
	ResourceGroupName  string `yaml:"resource_group_name"`
	StorageAccountName string `yaml:"storage_account_name"`
	ContainerName      string `yaml:"container_name"`
	KeyPrefix          string `yaml:"key_prefix,omitempty"`
}

// GCS stores state in a Google Cloud Storage bucket.
type GCS struct {
	Bucket string `yaml:"bucket"`
	Prefix string `yaml:"prefix,omitempty"`
}

// Load reads and checks a configuration file.
func Load(file string) (*File, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	f := &File{}
	if err := decoder.Decode(f); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if f.Version != Version {
		return nil, fmt.Errorf("%s: version %d isn't supported; this infraharvest reads version %d", file, f.Version, Version)
	}
	if err := f.Backend.check(); err != nil {
		return nil, fmt.Errorf("%s: backend: %w", file, err)
	}
	if err := f.Environments.check(); err != nil {
		return nil, fmt.Errorf("%s: environments: %w", file, err)
	}
	for i, m := range f.Modules {
		resolved, err := m.check(filepath.Dir(file))
		if err != nil {
			return nil, fmt.Errorf("%s: modules[%d]: %w", file, i, err)
		}
		f.Modules[i] = resolved
	}
	return f, nil
}

// check returns m with a directory'"'"'s path made absolute, from dir, or why
// m isn'"'"'t pinned: a generated root must call what was tested.
func (m Module) check(dir string) (Module, error) {
	switch {
	case m.Source == "":
		return m, errors.New("no source")
	case IsLocalModule(m.Source):
		if m.Version != "" {
			return m, fmt.Errorf("%s: a directory has no version", m.Source)
		}
		if !filepath.IsAbs(m.Source) {
			m.Source = filepath.Join(dir, m.Source)
		}
		abs, err := filepath.Abs(m.Source)
		if err != nil {
			return m, err
		}
		m.Source = abs
	case isGitSource(m.Source):
		ref := gitRef(m.Source)
		if ref == "" {
			return m, fmt.Errorf("%s: pin a tag or commit with ?ref=", m.Source)
		}
		if !commitSHA.MatchString(ref) && !versionTag(ref) {
			log.Printf("%s: ref %s may be a branch, which moves: pin a release tag or a commit, so that roots call what was tested", m.Source, ref)
		}
		if m.Version != "" {
			return m, fmt.Errorf("%s: a git source takes its version from ref", m.Source)
		}
	default:
		if _, err := version.NewVersion(m.Version); err != nil || strings.ContainsAny(m.Version, "<>=~, ") {
			return m, fmt.Errorf("%s: a registry module needs an exact version, not %q", m.Source, m.Version)
		}
	}
	return m, nil
}

// isGitSource reports whether a module source is a git repository's.
func isGitSource(source string) bool {
	return strings.Contains(source, "::") || strings.HasPrefix(source, "github.com/") || strings.HasPrefix(source, "bitbucket.org/")
}

// RegistryProvider returns the provider a registry module's source ends
// with, aws for example.com/platform/network/aws, or "" for a git
// repository's or a directory's module.
func (m Module) RegistryProvider() string {
	if m.Source == "" || IsLocalModule(m.Source) || isGitSource(m.Source) {
		return ""
	}
	address, _, _ := strings.Cut(m.Source, "//")
	parts := strings.Split(address, "/")
	if len(parts) != 3 && len(parts) != 4 {
		return ""
	}
	return parts[len(parts)-1]
}

// Immutable reports whether what m's source names can't change: a registry
// module at its exact version (see check), or a git repository at a
// commit. A tag or a branch can move, and a directory changes when it is
// edited.
func (m Module) Immutable() bool {
	switch {
	case m.Source == "" || IsLocalModule(m.Source):
		return false
	case isGitSource(m.Source):
		return commitSHA.MatchString(gitRef(m.Source))
	}
	return m.Version != ""
}

// IsLocalModule reports whether a module source is a directory.
func IsLocalModule(source string) bool {
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") || strings.HasPrefix(source, ".\\") || strings.HasPrefix(source, "..\\") || filepath.IsAbs(source)
}

func (b *Backend) check() error {
	if b == nil {
		return nil
	}
	kinds := 0
	var missing []string
	if b.S3 != nil {
		kinds++
		missing = append(missing, required(map[string]string{"bucket": b.S3.Bucket, "region": b.S3.Region})...)
	}
	if b.AzureRM != nil {
		kinds++
		missing = append(missing, required(map[string]string{
			"resource_group_name": b.AzureRM.ResourceGroupName, "storage_account_name": b.AzureRM.StorageAccountName, "container_name": b.AzureRM.ContainerName,
		})...)
	}
	if b.GCS != nil {
		kinds++
		missing = append(missing, required(map[string]string{"bucket": b.GCS.Bucket})...)
	}
	if kinds != 1 {
		return errors.New("set exactly one of s3, azurerm and gcs")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	return nil
}

func required(values map[string]string) []string {
	var missing []string
	for name, v := range values {
		if v == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// commandLineOnly are the flags that aren't settings: config names the
// file itself, and check, set in it, would make every run that reads it
// check its flags and import nothing.
var commandLineOnly = []string{"check", "config"}

// Apply sets the flags the command line didn't, from the file's settings
// and the provider's. It fails on a setting that isn't a flag, or is one
// of commandLineOnly, and on settings for a provider that is neither one
// of ProviderCommands nor provider, which would otherwise never apply.
func (f *File) Apply(flags *pflag.FlagSet, provider string) error {
	for _, name := range slices.Sorted(maps.Keys(f.Providers)) {
		if name != provider && !slices.Contains(ProviderCommands, name) {
			return fmt.Errorf("providers: unknown provider %q: use %s", name, strings.Join(ProviderCommands, ", "))
		}
	}
	for _, settings := range []map[string]any{f.Settings, f.Providers[provider]} {
		for _, name := range slices.Sorted(maps.Keys(settings)) {
			if slices.Contains(commandLineOnly, name) {
				return fmt.Errorf("%q is for the command line, not a setting of the configuration file", name)
			}
			if flags.Lookup(name) == nil {
				return fmt.Errorf("%q isn't a setting of infraharvest %s", name, provider)
			}
			if err := applySetting(flags, name, settings[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

// ApplyShared sets the flags names lists, of a command that runs for no
// one provider, such as drift, that the command line didn't, from the
// file's settings: the settings it takes of those the provider commands
// take, which mean the same for it. It leaves the file's other settings
// to the provider commands. A provider's setting of one of names is an
// error, as the command can't tell which provider's to take.
func (f *File) ApplyShared(flags *pflag.FlagSet, command string, names []string) error {
	for _, provider := range slices.Sorted(maps.Keys(f.Providers)) {
		for _, name := range slices.Sorted(maps.Keys(f.Providers[provider])) {
			if slices.Contains(names, name) {
				return fmt.Errorf("providers: %s: %s: infraharvest %s takes it for every provider, so set it under settings", provider, name, command)
			}
		}
	}
	for _, name := range names {
		value, ok := f.Settings[name]
		if !ok {
			continue
		}
		if err := applySetting(flags, name, value); err != nil {
			return err
		}
	}
	return nil
}

// applySetting sets the flag name to value, a setting of the file, unless
// the command line set it.
func applySetting(flags *pflag.FlagSet, name string, value any) error {
	flag := flags.Lookup(name)
	if flag.Changed {
		return nil
	}
	if items, ok := value.([]any); ok && flag.Value.Type() == "stringArray" {
		// A flag given once per value, such as --id: each item is one,
		// commas and all.
		if err := setEach(flags, name, items); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	s, err := flagValue(value)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := flags.Set(name, s); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// setEach sets the flag name once per item, as a flag given several times
// on the command line.
func setEach(flags *pflag.FlagSet, name string, items []any) error {
	for _, item := range items {
		value, err := flagValue(item)
		if err != nil {
			return err
		}
		if err := flags.Set(name, value); err != nil {
			return err
		}
	}
	return nil
}

// flagValue renders a YAML value the way the flag takes it on the command
// line: lists comma-separated.
func flagValue(v any) (string, error) {
	switch v := v.(type) {
	case string:
		return v, nil
	case bool, int, float64:
		return fmt.Sprint(v), nil
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			s, err := flagValue(item)
			if err != nil {
				return "", err
			}
			items = append(items, s)
		}
		return strings.Join(items, ","), nil
	default:
		return "", fmt.Errorf("unsupported value %v", v)
	}
}

// File renders backend.tf for the root at rootPath, the root's path in the
// output directory with forward slashes.
func (b *Backend) File(rootPath string) []byte {
	f := hclwrite.NewEmptyFile()
	terraform := f.Body().AppendNewBlock("terraform", nil).Body()
	switch {
	case b.S3 != nil:
		backend := terraform.AppendNewBlock("backend", []string{"s3"}).Body()
		backend.SetAttributeValue("bucket", cty.StringVal(b.S3.Bucket))
		backend.SetAttributeValue("key", cty.StringVal(path.Join(b.S3.KeyPrefix, rootPath, "terraform.tfstate")))
		backend.SetAttributeValue("region", cty.StringVal(b.S3.Region))
		backend.SetAttributeValue("encrypt", cty.True)
		if b.S3.KMSKeyID != "" {
			backend.SetAttributeValue("kms_key_id", cty.StringVal(b.S3.KMSKeyID))
		}
		backend.SetAttributeValue("use_lockfile", cty.True)
	case b.AzureRM != nil:
		backend := terraform.AppendNewBlock("backend", []string{"azurerm"}).Body()
		backend.SetAttributeValue("resource_group_name", cty.StringVal(b.AzureRM.ResourceGroupName))
		backend.SetAttributeValue("storage_account_name", cty.StringVal(b.AzureRM.StorageAccountName))
		backend.SetAttributeValue("container_name", cty.StringVal(b.AzureRM.ContainerName))
		backend.SetAttributeValue("key", cty.StringVal(path.Join(b.AzureRM.KeyPrefix, rootPath, "terraform.tfstate")))
	case b.GCS != nil:
		backend := terraform.AppendNewBlock("backend", []string{"gcs"}).Body()
		backend.SetAttributeValue("bucket", cty.StringVal(b.GCS.Bucket))
		backend.SetAttributeValue("prefix", cty.StringVal(path.Join(b.GCS.Prefix, rootPath)))
	}
	return hclwrite.Format(f.Bytes())
}

// StateURL returns where the backend File renders for the root at
// rootPath keeps its state, as --managed-state names state objects:
// s3://<bucket>/<key>, gs://<bucket>/<prefix>/default.tfstate, or
// https://<account>.blob.core.windows.net/<container>/<key>.
func (b *Backend) StateURL(rootPath string) string {
	switch {
	case b.S3 != nil:
		return "s3://" + b.S3.Bucket + "/" + path.Join(b.S3.KeyPrefix, rootPath, "terraform.tfstate")
	case b.AzureRM != nil:
		return "https://" + b.AzureRM.StorageAccountName + ".blob.core.windows.net/" + b.AzureRM.ContainerName + "/" + path.Join(b.AzureRM.KeyPrefix, rootPath, "terraform.tfstate")
	case b.GCS != nil:
		return "gs://" + b.GCS.Bucket + "/" + path.Join(b.GCS.Prefix, rootPath, "default.tfstate")
	}
	return ""
}

// gitRef returns the ref a git module source pins, from its query, as in
// git::https://example.com/modules.git//bucket?ref=v1.4.0, or "".
func gitRef(source string) string {
	_, query, ok := strings.Cut(source, "?")
	if !ok {
		return ""
	}
	// A subdirectory may follow the query.
	query, _, _ = strings.Cut(query, "//")
	values, err := url.ParseQuery(query)
	if err != nil {
		return ""
	}
	return values.Get("ref")
}

// commitSHA matches a full commit SHA, the one ref that can't move.
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// versionTag reports whether a ref reads as a release tag, such as v1.4.0
// or 2.1.
func versionTag(ref string) bool {
	_, err := version.NewVersion(strings.TrimPrefix(ref, "v"))
	return err == nil
}
