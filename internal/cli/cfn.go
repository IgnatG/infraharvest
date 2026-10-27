// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/pkg/cfn"
)

// cfn translates CloudFormation templates, so only a build with AWS has it.
func init() {
	awsCommands = append(awsCommands, newCfnCmd)
}

func newCfnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cfn",
		Short: "Translate CloudFormation templates into Terraform",
		Long: "Translate an AWS CloudFormation template into Terraform configuration, on its\n" +
			"own, with no cloud account: the template's intent, not live resources. To\n" +
			"take over a deployed stack's resources, import them with import aws\n" +
			"--cfn-stack, which carries the stack's template into the roots too.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	cmd.AddCommand(newCfnConvertCmd())
	return cmd
}

// cfnConvertOut is where cfn convert writes, unless --out says otherwise.
const cfnConvertOut = "cfn-terraform"

func newCfnConvertCmd() *cobra.Command {
	var out, stackName string
	var preventDestroy, allowPartial bool
	cmd := &cobra.Command{
		Use:   "convert <template>",
		Short: "Translate a CloudFormation template into Terraform files",
		Long: "Translate a CloudFormation template, JSON or YAML (- reads standard input),\n" +
			"into Terraform files for the AWS provider in --out (default " + cfnConvertOut + "):\n" +
			"variables.tf from its parameters, locals.tf from its mappings and conditions,\n" +
			"main.tf from its resources, outputs.tf from its outputs and data.tf for what\n" +
			"they read. What has no Terraform equivalent, such as a custom resource or a\n" +
			"macro, is a diagnostic on standard error and a comment in the files. It exits\n" +
			"1 when something couldn't be converted (3 with --allow-partial), and 2 when\n" +
			"the template can't be read.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return convertTemplate(args[0], out, cfn.Options{StackName: stackName, PreventDestroy: preventDestroy}, allowPartial, cmd.InOrStdin(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", cfnConvertOut, "directory to write the Terraform files into; files of the same names must not exist")
	cmd.Flags().StringVar(&stackName, "stack-name", "", "default of the stack_name variable that stands in for AWS::StackName")
	cmd.Flags().BoolVar(&preventDestroy, "prevent-destroy", false, "add lifecycle prevent_destroy to resources whose DeletionPolicy is Retain")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "exit 3 rather than 1 when something couldn't be converted")
	return cmd
}

// convertTemplate converts the template at path ("-" for stdin) into out,
// printing the diagnostics to diagnostics.
func convertTemplate(path, out string, opts cfn.Options, allowPartial bool, stdin io.Reader, diagnostics io.Writer) error {
	couldNotRun := func(err error) error { return &importer.ExitError{Code: report.ExitCouldNotRun, Err: err} }
	in := stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return couldNotRun(err)
		}
		defer f.Close()
		in = f
	}
	t, err := cfn.Parse(in)
	if err != nil {
		return couldNotRun(fmt.Errorf("%s: %w", path, err))
	}
	res, err := cfn.Convert(t, opts)
	if err != nil {
		return couldNotRun(fmt.Errorf("%s: %w", path, err))
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return couldNotRun(err)
	}
	// Lstat: a symbolic link there, dangling or not, is a file too, which
	// the write would follow out of out.
	for _, name := range res.FileNames() {
		if _, err := os.Lstat(filepath.Join(out, name)); err == nil {
			return couldNotRun(fmt.Errorf("%s already exists; use an empty --out directory", filepath.Join(out, name)))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return couldNotRun(err)
		}
	}
	for _, name := range res.FileNames() {
		if err := os.WriteFile(filepath.Join(out, name), res.Files[name], 0o644); err != nil {
			return couldNotRun(err)
		}
	}
	for _, d := range res.Diagnostics {
		fmt.Fprintln(diagnostics, d)
	}
	fmt.Fprintf(diagnostics, "wrote %d files into %s, with %d diagnostics\n", len(res.Files), out, len(res.Diagnostics))
	if res.HasErrors() {
		code := report.ExitIncomplete
		if allowPartial {
			code = report.ExitPartial
		}
		return &importer.ExitError{Code: code, Err: errors.New("some of the template couldn't be converted: see the diagnostics marked error")}
	}
	return nil
}
