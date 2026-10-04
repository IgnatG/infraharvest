// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// One import covers global services (region aws-global) and then each
// --regions region; each must list with its own region.
func TestGenerateConfigPerRegion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "") // generateConfig sets it; restore it afterwards
	configs = map[configKey]aws.Config{}
	t.Cleanup(func() { configs = map[configKey]aws.Config{} })

	for _, region := range []string{GlobalRegion, "us-east-1", GlobalRegion} {
		s := &AWSService{}
		s.SetArgs(map[string]interface{}{"region": region, "profile": ""})

		cfg, err := s.generateConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Region != region {
			t.Errorf("config for %s has region %q", region, cfg.Region)
		}
	}
}

// With credentials only in the environment, as from OIDC in CI, there is no
// shared config file: the default --profile must still work.
func TestGenerateConfigWithoutSharedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "missing-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "missing-credentials"))
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	configs = map[configKey]aws.Config{}
	t.Cleanup(func() { configs = map[configKey]aws.Config{} })

	s := &AWSService{}
	s.SetArgs(map[string]interface{}{"region": "us-east-1", "profile": "default"})
	if _, err := s.generateConfig(); err != nil {
		t.Errorf("want the environment's credentials used, got %v", err)
	}
}
