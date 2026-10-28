// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// conftest's JSON report, as conftest 0.71.1 writes it for a root whose
// main.tf has a bucket without versioning or an owner tag, and whose
// variables.tf passes.
const conftestReport = `[
	{
		"filename": "variables.tf",
		"namespace": "s3",
		"successes": 2
	},
	{
		"filename": "main.tf",
		"namespace": "s3",
		"successes": 0,
		"warnings": [
			{
				"msg": "aws_s3_bucket.logs has no owner tag",
				"metadata": {
					"query": "data.s3.warn"
				}
			}
		],
		"failures": [
			{
				"msg": "aws_s3_bucket.logs has no aws_s3_bucket_versioning",
				"metadata": {
					"query": "data.s3.deny"
				}
			}
		]
	}
]`

// Each policy's deny and warn are findings, of its namespace, at the
// file; files that pass have none.
func TestParseConftest(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		out  string
		want []Finding
	}{
		"failures and warnings": {conftestReport, []Finding{
			{Rule: "s3", Severity: "failure", Location: "main.tf", Message: "aws_s3_bucket.logs has no aws_s3_bucket_versioning"},
			{Rule: "s3", Severity: "warning", Location: "main.tf", Message: "aws_s3_bucket.logs has no owner tag"},
		}},
		"namespaces": {`[{"filename":"main.tf","namespace":"main","successes":0,"failures":[{"msg":"no public buckets","metadata":{"query":"data.main.deny"}}]},{"filename":"main.tf","namespace":"tags","successes":0,"warnings":[{"msg":"no team tag","metadata":{"query":"data.tags.warn"}}]}]`, []Finding{
			{Rule: "main", Severity: "failure", Location: "main.tf", Message: "no public buckets"},
			{Rule: "tags", Severity: "warning", Location: "main.tf", Message: "no team tag"},
		}},
		"everything passes": {`[{"filename":"main.tf","namespace":"s3","successes":3}]`, nil},
		"no files":          {`[]`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := parseConftest([]byte(tc.out))
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
	if _, err := parseConftest([]byte("Error: running test: load: loading policies: no policies found")); err == nil {
		t.Error("output that isn't conftest's JSON: got no error")
	}
}

// conftest is a scanner only with policies to check against, and runs
// with them, on the root's .tf files, with the HCL2 parser, in every
// namespace, and nothing that fetches policies.
func TestConftestScanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake conftest is a shell script")
	}
	bin := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > " + argsFile + "\nprintf '%s' '" + conftestReport + "'\n"
	if err := os.WriteFile(filepath.Join(bin, ConftestBinary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Only the fake: no scanner installed on the machine runs.
	t.Setenv("PATH", bin)
	names := func(scanners []Scanner) []string {
		var names []string
		for _, s := range scanners {
			names = append(names, s.Name)
		}
		return names
	}
	if got := names(DefaultScanners(nil)); slices.Contains(got, ConftestBinary) {
		t.Errorf("without policies: got %v, want no conftest", got)
	}
	policies := []string{filepath.Join(t.TempDir(), "org"), filepath.Join(t.TempDir(), "team")}
	scanners := DefaultScanners(policies)
	i := slices.IndexFunc(scanners, func(s Scanner) bool { return s.Name == ConftestBinary })
	if i < 0 {
		t.Fatalf("with policies: got %v, want conftest", names(scanners))
	}
	if scanners[i].Blocking {
		t.Error("conftest blocks: its findings are about the infrastructure, which the configuration mirrors")
	}

	dir := t.TempDir()
	for _, name := range []string{"main.tf", "variables.tf", "terraform.tfstate", ".terraform/modules/m/main.tf"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := scanners[i].Run(t.Context(), dir)
	if err != nil || len(findings) != 2 {
		t.Fatalf("got %v, %v, want the report's two findings", findings, err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"test", "--parser=hcl2", "--all-namespaces", "--output=json", "--no-color", "--no-fail", "--policy=" + policies[0], "--policy=" + policies[1], "--", "main.tf", "variables.tf"}
	if got := strings.Fields(string(args)); !reflect.DeepEqual(got, want) {
		t.Errorf("conftest ran with %v, want %v", got, want)
	}
}
