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
	"os"
	"path"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/spf13/pflag"
	"github.com/zclconf/go-cty/cty"
	"go.yaml.in/yaml/v3"
)

// Version is the configuration file format's version.
const Version = 1

// File is a configuration file.
type File struct {
	Version int `yaml:"version"`
	// Settings for every provider, by flag name: engine, terraform-path,
	// path-output, path-pattern, selection, all, allow-partial, ...
	Settings map[string]any `yaml:"settings,omitempty"`
	// Providers holds settings for one provider command, by flag name:
	// aws: { profile: prod, regions: [eu-west-2], resources: [vpc, s3] }.
	Providers map[string]map[string]any `yaml:"providers,omitempty"`
	// Backend is the state backend of every generated root.
	Backend *Backend `yaml:"backend,omitempty"`
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
	return f, nil
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

// Apply sets the flags the command line didn't, from the file's settings
// and the provider's. It fails on a setting that isn't a flag.
func (f *File) Apply(flags *pflag.FlagSet, provider string) error {
	for _, settings := range []map[string]any{f.Settings, f.Providers[provider]} {
		names := make([]string, 0, len(settings))
		for name := range settings {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			flag := flags.Lookup(name)
			if flag == nil {
				return fmt.Errorf("%q isn't a setting of infraharvest %s", name, provider)
			}
			if flag.Changed {
				continue
			}
			value, err := flagValue(settings[name])
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := flags.Set(name, value); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
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
