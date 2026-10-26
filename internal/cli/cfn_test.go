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
