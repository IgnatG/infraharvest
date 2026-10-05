// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/terraformutils"
)

// newVerifyCmd runs the verification gate again on generated roots, for
// example after people edit them.
func newVerifyCmd() *cobra.Command {
	var engineName, terraformPath, output string
	cmd := &cobra.Command{
		Use:   "verify [output-directory]",
		Short: "Check generated roots again: format, validate, plan, standards, scanners, secrets, determinism",
		Long: "Run the verification gate again on every root an import generated (default\n" +
			"directory: generated), for example after editing them. Roots are initialised\n" +
			"without their backend, so no access to the state is needed; the plan reads the\n" +
			"cloud. Set secret variables first, with TF_VAR_ environment variables or a\n" +
			".auto.tfvars file. Exit code 0 means every check passed, 1 that one failed.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := DefaultPathOutput
			if len(args) == 1 {
				out = args[0]
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			return verifyOutput(ctx, out, engineName, terraformPath, output, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&engineName, "engine", engineTerraform, "terraform or tofu")
	cmd.Flags().StringVar(&terraformPath, "terraform-path", "", "Terraform or OpenTofu binary (default: on PATH, else installed as for import)")
	cmd.Flags().StringVarP(&output, "output", "O", outputHCL, "hcl prints a summary; json prints each root's checks as JSON")
	return cmd
}

// verifiedRoot is one root's gate results, as --output json prints them.
type verifiedRoot struct {
	Path   string         `json:"path"`
	Checks []report.Check `json:"checks"`
}

func verifyOutput(ctx context.Context, out, engineName, terraformPath, format string, w io.Writer) error {
	if engineName != engineTerraform && engineName != engineTofu {
		return fmt.Errorf("--engine must be %s or %s, not %q", engineTerraform, engineTofu, engineName)
	}
	if format != outputHCL && format != outputJSON {
		return fmt.Errorf("--output must be %s or %s, not %q", outputHCL, outputJSON, format)
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
		if provider := rootProvider(out, dir); provider != nil {
			providerOpts := engineOptions(provider, &rootFiles{})
			opts.Omit, opts.StateOnly = providerOpts.Omit, providerOpts.StateOnly
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
			root.Checks = append(root.Checks, report.Check{Name: c.Name, Passed: c.Passed, Details: c.Details})
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
	if format == outputJSON {
		content, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", content)
		return err
	}
	for _, r := range results {
		status := "passed"
		var failedNames []string
		for _, c := range r.Checks {
			if !c.Passed {
				failedNames = append(failedNames, c.Name)
			}
		}
		if len(failedNames) > 0 {
			status = "failed " + strings.Join(failedNames, ", ")
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", r.Path, status); err != nil {
			return err
		}
	}
	return nil
}

// generatedRoots returns the directories under out that an import
// generated, sorted: those with a generated.tf, outside .terraform and the
// generated modules.
func generatedRoots(out string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".terraform" || (path != out && d.Name() == engine.ModulesDirName && filepath.Dir(path) == filepath.Clean(out))) {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == engine.GeneratedFileName {
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

// rootProvider returns the provider a root belongs to, from the layout's
// first directory ({provider}/...), or nil.
func rootProvider(out, dir string) terraformutils.ProviderGenerator {
	first, _, _ := strings.Cut(relativePath(out, dir), "/")
	entry, ok := providerRegistry[first]
	if !ok {
		return nil
	}
	return entry.newProvider()
}
