// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/pkg/cfn"
)

func TestCfnConvert(t *testing.T) {
	out := filepath.Join(t.TempDir(), "tf")
	template := "Resources:\n  Logs:\n    Type: AWS::S3::Bucket\n    Properties:\n      BucketName: logs\n"
	var diags bytes.Buffer
	if err := convertTemplate("-", out, cfn.Options{}, false, strings.NewReader(template), &diags); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(out, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), `resource "aws_s3_bucket" "logs"`) {
		t.Errorf("main.tf:\n%s", main)
	}
	if !strings.Contains(diags.String(), "wrote 1 files") {
		t.Errorf("diagnostics: %s", diags.String())
	}

	// A second run doesn't overwrite what the first wrote.
	err = convertTemplate("-", out, cfn.Options{}, false, strings.NewReader(template), &diags)
	var exit *importer.ExitError
	if !errors.As(err, &exit) || exit.Code != report.ExitCouldNotRun {
		t.Errorf("err = %v, want exit %d", err, report.ExitCouldNotRun)
	}

	// What can't be converted exits 1, or 3 with --allow-partial.
	custom := "Resources:\n  Hook:\n    Type: Custom::Hook\n"
	for allow, want := range map[bool]int{false: report.ExitIncomplete, true: report.ExitPartial} {
		err := convertTemplate("-", t.TempDir(), cfn.Options{}, allow, strings.NewReader(custom), &diags)
		if !errors.As(err, &exit) || exit.Code != want {
			t.Errorf("allow-partial %v: err = %v, want exit %d", allow, err, want)
		}
	}

	if err := convertTemplate(filepath.Join(t.TempDir(), "missing.yaml"), out, cfn.Options{}, false, nil, &diags); !errors.As(err, &exit) || exit.Code != report.ExitCouldNotRun {
		t.Errorf("missing template: err = %v", err)
	}
}

// cfn convert through the command line: flags, standard input and exit
// codes.
func TestCfnConvertCommand(t *testing.T) {
	run := func(stdin string, args ...string) (string, error) {
		cmd := NewCmdRoot()
		var stderr bytes.Buffer
		cmd.SetArgs(args)
		cmd.SetIn(strings.NewReader(stdin))
		cmd.SetErr(&stderr)
		cmd.SetOut(&stderr)
		err := cmd.Execute()
		return stderr.String(), err
	}
	template := "Parameters:\n  Name: {Type: String}\nResources:\n  Logs:\n    Type: AWS::S3::Bucket\n    DeletionPolicy: Retain\n    Properties:\n      BucketName: !Sub \"${AWS::StackName}-${Name}\"\n"
	out := filepath.Join(t.TempDir(), "tf")
	stderr, err := run(template, "cfn", "convert", "-", "-o", out, "--stack-name", "app", "--prevent-destroy")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	vars, _ := os.ReadFile(filepath.Join(out, "variables.tf"))
	main, _ := os.ReadFile(filepath.Join(out, "main.tf"))
	if !strings.Contains(string(vars), `"app"`) || !strings.Contains(string(main), "prevent_destroy = true") {
		t.Errorf("variables.tf:\n%s\nmain.tf:\n%s", vars, main)
	}

	// A property the mapper leaves out on purpose, such as a bucket's
	// versioning, is a warning, which doesn't fail the command.
	omitted := filepath.Join(t.TempDir(), "omitted.yaml")
	if err := os.WriteFile(omitted, []byte("Resources:\n  B:\n    Type: AWS::S3::Bucket\n    Properties:\n      VersioningConfiguration: {Status: Enabled}\n      BucketEncryption: {ServerSideEncryptionConfiguration: [{ServerSideEncryptionByDefault: {SSEAlgorithm: AES256}}]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr, err = run("", "cfn", "convert", omitted, "--out", t.TempDir())
	if err != nil || !strings.Contains(stderr, "warning: Resources.B.Properties.VersioningConfiguration") || !strings.Contains(stderr, "warning: Resources.B.Properties.BucketEncryption") {
		t.Errorf("err = %v, stderr:\n%s", err, stderr)
	}

	file := filepath.Join(t.TempDir(), "t.yaml")
	if err := os.WriteFile(file, []byte("Resources:\n  B:\n    Type: Custom::Seeder\n    Properties:\n      ServiceToken: arn:aws:lambda:x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var exit *importer.ExitError
	// A custom resource isn't converted.
	stderr, err = run("", "cfn", "convert", file, "--out", t.TempDir())
	if !errors.As(err, &exit) || exit.Code != report.ExitIncomplete || !strings.Contains(stderr, "error: Resources.B: a custom resource") {
		t.Errorf("err = %v, stderr:\n%s", err, stderr)
	}
	_, err = run("", "cfn", "convert", file, "--out", t.TempDir(), "--allow-partial")
	if !errors.As(err, &exit) || exit.Code != report.ExitPartial {
		t.Errorf("--allow-partial: err = %v", err)
	}
	if _, err := run("", "cfn", "convert"); err == nil {
		t.Error("convert without a template succeeded")
	}
}
