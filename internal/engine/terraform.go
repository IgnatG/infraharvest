// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// Binary describes an engine binary: Terraform or OpenTofu, which run the
// same configuration through the same CLI.
type Binary struct {
	// Name is the executable's name, without .exe.
	Name string
	// Registry is the host provider sources without one resolve to.
	Registry string
	// Minimum is the first release with import blocks and
	// `plan -generate-config-out`.
	Minimum *version.Version
	// install downloads the latest release into a directory and returns the
	// binary's path; nil if the engine isn't downloaded automatically.
	install func(ctx context.Context, dir string) (string, error)
}

var (
	// TerraformBinary is HashiCorp Terraform, downloaded if not installed.
	TerraformBinary = Binary{
		Name:     "terraform",
		Registry: DefaultRegistry,
		Minimum:  version.Must(version.NewVersion("1.5.0")),
		install:  installLatestTerraform,
	}
	// TofuBinary is OpenTofu. It must be installed.
	TofuBinary = Binary{
		Name:     "tofu",
		Registry: "registry.opentofu.org",
		Minimum:  version.Must(version.NewVersion("1.6.0")),
	}
)

// findMu serializes Binary.Find (see there).
var findMu sync.Mutex

// versionOf reports the version of the engine binary at execPath.
type versionOf func(ctx context.Context, execPath string) (*version.Version, error)

// FindTerraform finds Terraform; see Binary.Find.
func FindTerraform(ctx context.Context, explicitPath, cacheDir string) (string, error) {
	return TerraformBinary.Find(ctx, explicitPath, cacheDir)
}

// Find returns a binary of at least b.Minimum, in this order: explicitPath
// (which must qualify), b.Name on PATH, a binary cached in cacheDir, or,
// for engines downloaded automatically, the latest release downloaded into
// cacheDir. Terraform downloads are verified against HashiCorp's signed
// checksums.
func (b Binary) Find(ctx context.Context, explicitPath, cacheDir string) (string, error) {
	// One at a time: imports running in parallel would download into the
	// same cache.
	findMu.Lock()
	defer findMu.Unlock()
	return b.find(ctx, explicitPath, cacheDir, binaryVersion)
}

func (b Binary) find(ctx context.Context, explicitPath, cacheDir string, versionOf versionOf) (string, error) {
	if explicitPath != "" {
		if err := b.checkVersion(ctx, explicitPath, versionOf); err != nil {
			return "", err
		}
		return explicitPath, nil
	}
	candidates := []string{filepath.Join(cacheDir, b.fileName())}
	if onPath, err := exec.LookPath(b.Name); err == nil {
		candidates = append([]string{onPath}, candidates...)
	}
	var skipped []error
	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		err := b.checkVersion(ctx, path, versionOf)
		if err == nil {
			return path, nil
		}
		// Say why an installed binary isn't used, rather than download
		// another quietly.
		log.Printf("%s: not using %s: %v", b.Name, path, err)
		skipped = append(skipped, err)
	}
	if b.install == nil {
		err := fmt.Errorf("%s %s or newer not found on PATH; install it, or pass its path", b.Name, b.Minimum)
		if len(skipped) > 0 {
			err = fmt.Errorf("%w: %w", err, errors.Join(skipped...))
		}
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	return b.install(ctx, cacheDir)
}

func (b Binary) checkVersion(ctx context.Context, execPath string, versionOf versionOf) error {
	v, err := versionOf(ctx, execPath)
	if err != nil {
		return fmt.Errorf("%s at %s: %w", b.Name, execPath, err)
	}
	if v.LessThan(b.Minimum) {
		return fmt.Errorf("%s at %s is %s; %s or newer is required", b.Name, execPath, v, b.Minimum)
	}
	return nil
}

func (b Binary) fileName() string {
	if runtime.GOOS == "windows" {
		return b.Name + ".exe"
	}
	return b.Name
}

// binaryVersion asks the binary at execPath for its version. OpenTofu
// answers `version -json` like Terraform.
func binaryVersion(ctx context.Context, execPath string) (*version.Version, error) {
	tf, err := tfexec.NewTerraform(os.TempDir(), execPath)
	if err != nil {
		return nil, err
	}
	v, _, err := tf.Version(ctx, true)
	return v, err
}

func installLatestTerraform(ctx context.Context, dir string) (string, error) {
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

// initMu serializes terraform init within the process. The roots of
// several accounts can generate in parallel, sharing one plugin cache,
// which Terraform doesn't guarantee to be safe for concurrent installs.
var initMu sync.Mutex

// initTerraform runs terraform init in tf's working directory, one at a
// time (see initMu).
func initTerraform(ctx context.Context, tf Terraform, opts ...tfexec.InitOption) error {
	initMu.Lock()
	defer initMu.Unlock()
	return tf.Init(ctx, opts...)
}
