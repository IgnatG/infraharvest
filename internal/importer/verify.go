// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// verifiedRoot is one root's gate results, as --format json prints them.
type verifiedRoot struct {
	Path   string         `json:"path"`
	Checks []report.Check `json:"checks"`
}

func Verify(ctx context.Context, options VerifyOptions, w io.Writer) error {
	out, engineName, terraformPath, format, catalogueDir := options.Out, options.Engine, options.TerraformPath, options.Format, options.Catalogue
	if engineName != EngineTerraform && engineName != EngineTofu {
		return fmt.Errorf("--engine must be %s or %s, not %q", EngineTerraform, EngineTofu, engineName)
	}
	if format != FormatText && format != FormatJSON {
		return fmt.Errorf("--format must be %s or %s, not %q", FormatText, FormatJSON, format)
	}
	roots, err := generatedRoots(out)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return fmt.Errorf("no generated roots in %s", out)
	}
	binary := engineBinary(engineName)
	cacheDir, err := infraharvestCacheDir()
	if err != nil {
		return err
	}
	execPath, err := binary.Find(ctx, terraformPath, filepath.Join(cacheDir, binary.Name))
	if err != nil {
		return err
	}
	scanners := engine.DefaultScanners()
	var results []verifiedRoot
	var failed []string
	for _, dir := range roots {
		opts := engine.Options{Scanners: scanners}
		if provider := rootProvider(out, dir, options.Provider); provider != nil {
			cat, err := providerCatalogue(provider, catalogueDir)
			if err != nil {
				return err
			}
			v := lockedProviderVersion(dir, binary.Registry, provider)
			opts.Omit, opts.StateOnly = cat.OmitFor(v), cat.StateOnlyFor(v)
		}
		tf, err := engine.NewTerraform(dir, execPath, filepath.Join(cacheDir, "plugins"), nil)
		if err != nil {
			return err
		}
		gate, err := engine.Verify(ctx, tf, dir, opts)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		root := verifiedRoot{Path: relativePath(out, dir)}
		for _, c := range gate {
			root.Checks = append(root.Checks, report.Check{Name: c.Name, Passed: c.Passed, Skipped: c.Skipped, Details: c.Details})
			if !c.Passed {
				failed = append(failed, fmt.Sprintf("%s: %s failed: %s", root.Path, c.Name, strings.Join(c.Details, "; ")))
			}
		}
		results = append(results, root)
	}
	if err := writeVerified(w, results, format); err != nil {
		return err
	}
	if len(failed) > 0 {
		return &ExitError{Code: report.ExitIncomplete, Err: errors.New(strings.Join(failed, "\n"))}
	}
	return nil
}

func writeVerified(w io.Writer, results []verifiedRoot, format string) error {
	if format == FormatJSON {
		content, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", content)
		return err
	}
	for _, r := range results {
		status := "passed"
		var failedNames, skippedNames []string
		for _, c := range r.Checks {
			switch {
			case !c.Passed:
				failedNames = append(failedNames, c.Name)
			case c.Skipped:
				skippedNames = append(skippedNames, c.Name)
			}
		}
		if len(failedNames) > 0 {
			status = "failed " + strings.Join(failedNames, ", ")
		}
		if len(skippedNames) > 0 {
			status += "; skipped " + strings.Join(skippedNames, ", ")
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", r.Path, status); err != nil {
			return err
		}
	}
	return nil
}

// generatedRoots returns the directories under out that an import
// generated, sorted: those with a providers.tf, outside .terraform, the
// generated modules and the bootstrap root.
func generatedRoots(out string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		topLevel := path != out && filepath.Dir(path) == filepath.Clean(out)
		if d.IsDir() && (d.Name() == ".terraform" || (topLevel && (d.Name() == engine.ModulesDirName || d.Name() == config.BootstrapDir))) {
			return filepath.SkipDir
		}
		// Every root configures its provider; modules don't.
		if !d.IsDir() && d.Name() == engine.ProvidersFileName {
			roots = append(roots, filepath.Dir(path))
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no output directory %s", out)
	}
	sort.Strings(roots)
	return roots, err
}

// VerifyOptions say what Verify checks and how.
type VerifyOptions struct {
	Out           string // the output directory of the import
	Engine        string // EngineTerraform or EngineTofu
	TerraformPath string // the binary, or "" to find or install one
	Format        string // FormatText or FormatJSON
	Catalogue     string // directory of catalogue files the import used, if any
	// Provider returns a new provider of the given name, or nil when it
	// isn't compiled in: each root's catalogue comes from its provider.
	Provider func(name string) harvest.Provider
}

// rootProvider returns the provider a root belongs to, from the layout's
// first directory ({provider}/...), or nil.
func rootProvider(out, dir string, provider func(string) harvest.Provider) harvest.Provider {
	if provider == nil {
		return nil
	}
	first, _, _ := strings.Cut(relativePath(out, dir), "/")
	return provider(first)
}

// lockedProviderVersion returns the version of provider that the lock file
// of dir selected, through registry, or nil if it records none: the
// catalogue entries that depend on the provider's version then don't
// apply.
func lockedProviderVersion(dir, registry string, provider harvest.Provider) *goversion.Version {
	lock, err := os.ReadFile(filepath.Join(dir, engine.LockFileName))
	if err != nil {
		return nil
	}
	v, err := goversion.NewVersion(engine.LockedVersion(lock, qualifiedSource(registry, engineProvider(provider).Source)))
	if err != nil {
		return nil
	}
	return v
}
