// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// MinTerraformVersion is the first release with import blocks and
// `plan -generate-config-out`.
var MinTerraformVersion = version.Must(version.NewVersion("1.5.0"))

// versionOf reports the version of the Terraform binary at execPath.
type versionOf func(ctx context.Context, execPath string) (*version.Version, error)

// FindTerraform returns a Terraform binary at least MinTerraformVersion, in
// this order: explicitPath (which must qualify), terraform on PATH, a binary
// cached in cacheDir, or the latest release downloaded into cacheDir.
// Downloads are verified against HashiCorp's signed checksums.
func FindTerraform(ctx context.Context, explicitPath, cacheDir string) (string, error) {
	return findTerraform(ctx, explicitPath, cacheDir, terraformVersion, installLatest)
}

func findTerraform(ctx context.Context, explicitPath, cacheDir string, versionOf versionOf, install func(context.Context, string) (string, error)) (string, error) {
	if explicitPath != "" {
		if err := checkVersion(ctx, explicitPath, versionOf); err != nil {
			return "", err
		}
		return explicitPath, nil
	}
	candidates := []string{filepath.Join(cacheDir, binaryName())}
	if onPath, err := exec.LookPath("terraform"); err == nil {
		candidates = append([]string{onPath}, candidates...)
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil && checkVersion(ctx, path, versionOf) == nil {
			return path, nil
		}
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	return install(ctx, cacheDir)
}

func checkVersion(ctx context.Context, execPath string, versionOf versionOf) error {
	v, err := versionOf(ctx, execPath)
	if err != nil {
		return fmt.Errorf("terraform at %s: %w", execPath, err)
	}
	if v.LessThan(MinTerraformVersion) {
		return fmt.Errorf("terraform at %s is %s; %s or newer is required", execPath, v, MinTerraformVersion)
	}
	return nil
}

func terraformVersion(ctx context.Context, execPath string) (*version.Version, error) {
	tf, err := tfexec.NewTerraform(os.TempDir(), execPath)
	if err != nil {
		return nil, err
	}
	v, _, err := tf.Version(ctx, true)
	return v, err
}

func installLatest(ctx context.Context, dir string) (string, error) {
	src := &releases.LatestVersion{Product: product.Terraform, InstallDir: dir}
	if err := src.Validate(); err != nil {
		return "", err
	}
	path, err := src.Install(ctx)
	if err != nil {
		return "", fmt.Errorf("install terraform: %w", err)
	}
	return path, nil
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "terraform.exe"
	}
	return "terraform"
}
