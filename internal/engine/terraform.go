// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/filelock"
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

// initSlot serializes terraform init within the process. The roots of
// several accounts can generate in parallel, sharing one plugin cache,
// which Terraform doesn't guarantee to be safe for concurrent installs;
// other processes wait for the cache's lock (see lockPluginCache). It is a
// channel rather than a mutex, so that a root waiting for its turn stops
// waiting when its context is done.
var initSlot = make(chan struct{}, 1)

// lockPluginCache waits until this process holds the lock of the plugin
// cache dir, or ctx is done. The lock is a file next to the cache, named
// after it with .lock, where Terraform, which reads the cache, never
// looks.
func lockPluginCache(ctx context.Context, dir string) (*filelock.Lock, error) {
	path := filepath.Clean(dir) + ".lock"
	lock, err := filelock.Wait(ctx, path, func() {
		log.Printf("waiting for another process to finish installing providers into %s", dir)
	})
	if err != nil {
		return nil, fmt.Errorf("lock the plugin cache %s: %w", dir, err)
	}
	return lock, nil
}

// initTerraform runs terraform init in tf's working directory, one at a
// time (see initSlot).
func initTerraform(ctx context.Context, tf Terraform, opts ...tfexec.InitOption) error {
	select {
	case initSlot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-initSlot }()
	return tf.Init(ctx, opts...)
}

// timeoutError is the error of a Terraform command that ran longer than
// it may (see Options.CommandTimeout).
type timeoutError struct {
	timeout time.Duration
}

func (e *timeoutError) Error() string {
	return fmt.Sprintf("timed out after %s", e.timeout)
}

// Unwrap makes a timeout a context.DeadlineExceeded.
func (e *timeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

// timedTerraform is a Terraform whose every command runs for at most
// timeout. A command still running then is stopped, and fails with a
// timeoutError, unless the caller's context was done first.
type timedTerraform struct {
	Terraform
	timeout time.Duration
}

// withTimeout returns tf with each command bounded by timeout (see
// timedTerraform), or tf itself if timeout is 0 or tf is bounded already.
func withTimeout(tf Terraform, timeout time.Duration) Terraform {
	if timeout <= 0 {
		return tf
	}
	if _, ok := tf.(*timedTerraform); ok {
		return tf
	}
	return &timedTerraform{Terraform: tf, timeout: timeout}
}

// bounded runs command with a context that ends after t.timeout.
func (t *timedTerraform) bounded(ctx context.Context, command func(context.Context) error) error {
	timed, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	err := command(timed)
	if err != nil && ctx.Err() == nil && errors.Is(timed.Err(), context.DeadlineExceeded) {
		return &timeoutError{timeout: t.timeout}
	}
	return err
}

func (t *timedTerraform) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	return t.bounded(ctx, func(ctx context.Context) error {
		return t.Terraform.Init(ctx, opts...)
	})
}

func (t *timedTerraform) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	var changes bool
	err := t.bounded(ctx, func(ctx context.Context) error {
		var err error
		changes, err = t.Terraform.PlanJSON(ctx, w, opts...)
		return err
	})
	return changes, err
}

func (t *timedTerraform) Validate(ctx context.Context) (*tfjson.ValidateOutput, error) {
	var out *tfjson.ValidateOutput
	err := t.bounded(ctx, func(ctx context.Context) error {
		var err error
		out, err = t.Terraform.Validate(ctx)
		return err
	})
	return out, err
}

func (t *timedTerraform) ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error) {
	var schemas *tfjson.ProviderSchemas
	err := t.bounded(ctx, func(ctx context.Context) error {
		var err error
		schemas, err = t.Terraform.ProvidersSchema(ctx)
		return err
	})
	return schemas, err
}

func (t *timedTerraform) ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error) {
	var p *tfjson.Plan
	err := t.bounded(ctx, func(ctx context.Context) error {
		var err error
		p, err = t.Terraform.ShowPlanFile(ctx, planPath, opts...)
		return err
	})
	return p, err
}

func (t *timedTerraform) FormatCheck(ctx context.Context, opts ...tfexec.FormatOption) (bool, []string, error) {
	var formatted bool
	var unformatted []string
	err := t.bounded(ctx, func(ctx context.Context) error {
		var err error
		formatted, unformatted, err = t.Terraform.FormatCheck(ctx, opts...)
		return err
	})
	return formatted, unformatted, err
}

// emptyStatePattern names the temporary directories of emptyState.
const emptyStatePattern = "infraharvest-state-"

// emptyState returns the path of a state file that doesn't exist, in a
// new temporary directory, and a function that removes the directory.
// Plans with it read no state (see statelessTerraform), and its lock is
// in that directory.
func emptyState() (string, func(), error) {
	empty, err := os.MkdirTemp("", emptyStatePattern)
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(empty, stagedStateFile), func() { _ = os.RemoveAll(empty) }, nil
}

// withoutLocalState returns tf for dir, whose plans read no state: if dir
// has the local state of a root applied without a backend (see
// LocalStateFile), they plan with an empty state outside dir instead (see
// emptyState), as Generate plans a root with a backend, whose state it
// never reaches. With dir's state, Terraform would skip the import of
// each resource the state has, and so generate no configuration for it;
// the root's own plans, by whoever applies it, skip those imports, and
// its moved blocks move the state's resources. The state is never
// written, as a plan doesn't write state. The function it returns removes
// the empty state's directory.
func withoutLocalState(tf Terraform, dir string) (Terraform, func(), error) {
	if _, err := os.Stat(filepath.Join(dir, stagedStateFile)); errors.Is(err, fs.ErrNotExist) {
		return tf, func() {}, nil
	} else if err != nil {
		return nil, nil, err
	}
	state, remove, err := emptyState()
	if err != nil {
		return nil, nil, err
	}
	return &statelessTerraform{Terraform: tf, state: state}, remove, nil
}

// statelessTerraform is a Terraform whose plans read the state at state,
// which doesn't exist (see emptyState), rather than the state of the
// directory it plans, in terraform.tfstate or a workspace's: -state
// takes the place of both.
type statelessTerraform struct {
	Terraform
	state string
}

func (s *statelessTerraform) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	// -state is a legacy flag, which Terraform and OpenTofu still take for
	// the local backend; a backend block for the empty state instead would
	// change the backend that terraform init records in the root.
	state := tfexec.State(s.state) //nolint:staticcheck // see above
	return s.Terraform.PlanJSON(ctx, w, append(slices.Clone(opts), state)...)
}
