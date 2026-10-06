// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"os"
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
	t.Setenv("AWS_REGION", "")
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

// Terraform gets the credentials a profile resolves to through its own
// environment (TerraformEnv), not the process's.
func TestTerraformEnv(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"config":      "[profile prod]\nregion = eu-west-2\n",
		"credentials": "[prod]\naws_access_key_id = AKIDPROD\naws_secret_access_key = prod-secret\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_REGION"} {
		t.Setenv(k, "")
	}
	configs = map[configKey]aws.Config{}
	t.Cleanup(func() { configs = map[configKey]aws.Config{} })
	p := &AWSProvider{region: "eu-west-2", profile: "prod"}

	env, err := p.TerraformEnv(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if env["AWS_ACCESS_KEY_ID"] != "AKIDPROD" || env["AWS_SECRET_ACCESS_KEY"] != "prod-secret" || env["AWS_SESSION_TOKEN"] != "" {
		t.Errorf("env: %v", env)
	}
	if profile, ok := env["AWS_PROFILE"]; !ok || profile != "" {
		t.Errorf("AWS_PROFILE: %q, %t; want it empty, so that Terraform uses the keys", profile, ok)
	}
	if region, ok := env["AWS_REGION"]; ok {
		t.Errorf("AWS_REGION: %q; the provider block names the region", region)
	}

	// Without --regions, the region the profile resolves to.
	defaultRegion := &AWSProvider{}
	if err := defaultRegion.Init([]string{NoRegion, "prod"}); err != nil {
		t.Fatal(err)
	}
	env, err = defaultRegion.TerraformEnv(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if env["AWS_REGION"] != "eu-west-2" {
		t.Errorf("AWS_REGION: %q, want the profile's eu-west-2", env["AWS_REGION"])
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_PROFILE") != "" {
		t.Error("the process environment changed")
	}
}
