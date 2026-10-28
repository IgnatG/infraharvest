// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
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
			{Rule: "s3", Severity: "failure", Location: "main.tf", Message: "aws_s3_bucket.logs has no aws_s3_bucket_versioning", Resource: "aws_s3_bucket.logs"},
			{Rule: "s3", Severity: "warning", Location: "main.tf", Message: "aws_s3_bucket.logs has no owner tag", Resource: "aws_s3_bucket.logs"},
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
	capsFile := filepath.Join(t.TempDir(), "capabilities.json")
	// It keeps a copy of the capabilities it is given, which the scanner
	// removes once it has run, with the shell's builtins only, as PATH has
	// nothing else.
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; case \"$a\" in --capabilities=*) while IFS= read -r l || [ -n \"$l\" ]; do printf '%s\\n' \"$l\"; done < \"${a#--capabilities=}\" > " + capsFile + ";; esac; done > " + argsFile + "\nprintf '%s' '" + conftestReport + "'\n"
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
	got := strings.Fields(string(args))
	capabilities := ""
	for i, a := range got {
		if path, ok := strings.CutPrefix(a, "--capabilities="); ok {
			capabilities, got[i] = path, "--capabilities="
		}
	}
	want := []string{"test", "--parser=hcl2", "--all-namespaces", "--output=json", "--no-color", "--no-fail", "--capabilities=", "--policy=" + policies[0], "--policy=" + policies[1], "--", "main.tf", "variables.tf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conftest ran with %v, want %v", got, want)
	}
	if _, err := os.Stat(capabilities); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the capabilities file %q is left behind: %v", capabilities, err)
	}
	content, err := os.ReadFile(capsFile)
	if err != nil {
		t.Fatal(err)
	}
	checkCapabilities(t, content)
}

// checkCapabilities checks the capabilities conftest runs with have
// OPA's built-in functions, but none of those deniedCalls calls, and no
// host to reach.
func checkCapabilities(t *testing.T, content []byte) {
	t.Helper()
	var capabilities struct {
		Builtins []struct {
			Name string `json:"name"`
		} `json:"builtins"`
		AllowNet *[]string `json:"allow_net"`
		Features []string  `json:"features"`
	}
	if err := json.Unmarshal(content, &capabilities); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, b := range capabilities.Builtins {
		names = append(names, b.Name)
	}
	for _, name := range []string{"sprintf", "contains", "net.cidr_contains", "json.unmarshal", "print", "trace", "io.jwt.decode_verify"} {
		if !slices.Contains(names, name) {
			t.Errorf("capabilities lack %s", name)
		}
	}
	for name := range deniedCalls {
		if slices.Contains(names, name) {
			t.Errorf("capabilities have %s, which reaches the network or reads files", name)
		}
	}
	if capabilities.AllowNet == nil || len(*capabilities.AllowNet) != 0 {
		t.Errorf("allow_net: got %v, want no host", capabilities.AllowNet)
	}
	if !slices.Contains(capabilities.Features, "rego_v1") {
		t.Errorf("capabilities lack OPA's features: %v", capabilities.Features)
	}
}

// Every denied function is OPA's, or one of conftest's own, which OPA's
// capabilities don't have, so that none is denied by a name nothing has,
// and has a call to check; the capabilities conftest runs with have none
// of the functions those calls call.
func TestDeniedBuiltins(t *testing.T) {
	t.Parallel()
	var opa struct {
		Builtins []struct {
			Name string `json:"name"`
		} `json:"builtins"`
	}
	if err := json.Unmarshal(opaCapabilities, &opa); err != nil {
		t.Fatal(err)
	}
	conftests := []string{"parse_config", "parse_config_file", "parse_combined_config_files"}
	for _, name := range deniedBuiltins {
		isOPA := slices.ContainsFunc(opa.Builtins, func(b struct {
			Name string `json:"name"`
		}) bool {
			return b.Name == name
		})
		if isOPA == slices.Contains(conftests, name) {
			t.Errorf("%s is neither OPA's nor conftest's own", name)
		}
		if _, ok := deniedCalls[name]; !ok {
			t.Errorf("no call of %s to check", name)
		}
	}
	content, err := conftestCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	checkCapabilities(t, content)
}

// deniedCalls call each of deniedBuiltins as a policy would to send the
// configuration somewhere, or to read a file conftest wasn't given.
var deniedCalls = map[string]string{
	"http.send":                   `http.send({"method": "POST", "url": "http://127.0.0.1:9/", "body": input, "raise_error": false})`,
	"net.lookup_ip_addr":          `net.lookup_ip_addr("example.com")`,
	"json.match_schema":           `json.match_schema({}, {"$ref": "file:///etc/hostname"})`,
	"json.verify_schema":          `json.verify_schema({"$ref": "http://127.0.0.1:9/schema.json"})`,
	"opa.runtime":                 `opa.runtime()`,
	"parse_config":                `parse_config("jsonnet", "importstr '/etc/hostname'")`,
	"parse_config_file":           `parse_config_file("/etc/hostname")`,
	"parse_combined_config_files": `parse_combined_config_files(["/etc/hostname"])`,
}

// With conftest installed, a policy that calls a denied function doesn't
// compile, and one that doesn't runs.
func TestConftestPoliciesOffTheNetwork(t *testing.T) {
	path, err := exec.LookPath(ConftestBinary)
	if err != nil {
		t.Skip("conftest isn't installed")
	}
	dir := writeFindingsRoot(t)
	policy := func(rego string) []string {
		policies := t.TempDir()
		if err := os.WriteFile(filepath.Join(policies, "p.rego"), []byte(rego), 0o644); err != nil {
			t.Fatal(err)
		}
		return []string{policies}
	}
	offline := policy(`package p

import rego.v1

deny contains {"msg": sprintf("%s has no owner tag", [name]), "resource": sprintf("aws_s3_bucket.%s", [name])} if {
	some name, blocks in input.resource.aws_s3_bucket
	not blocks[0].tags.owner
	json.unmarshal("{}") == {}
}
`)
	findings, err := conftestScanner(path, offline).Run(t.Context(), dir)
	if err != nil || len(findings) != 1 || findings[0].Resource != "aws_s3_bucket.logs" {
		t.Errorf("got %+v, %v, want the bucket's finding", findings, err)
	}
	for name, call := range deniedCalls {
		denied := policy("package p\n\nimport rego.v1\n\ndeny contains sprintf(\"%v\", [x]) if {\n\tx := " + call + "\n}\n")
		if _, err := conftestScanner(path, denied).Run(t.Context(), dir); err == nil || !strings.Contains(err.Error(), "undefined function "+name) {
			t.Errorf("a policy that calls %s: got %v, want it not to compile", name, err)
		}
	}
}

// The capabilities file is OPA's, which is Apache-2.0: the NOTICE names
// it, with the OPA release it is copied from (opaVersion) and where
// that release's file is, and points at the licence, which REUSE.toml
// gives the file.
func TestNoticeNamesOPACapabilities(t *testing.T) {
	t.Parallel()
	for path, want := range map[string][]string{
		filepath.Join("..", "..", "..", "NOTICE"): {
			"internal/engine/gate/conftest_capabilities.json",
			"from OPA " + opaVersion + " (capabilities/v" + opaVersion + ".json)",
			"https://github.com/open-policy-agent/opa/blob/v" + opaVersion + "/capabilities/v" + opaVersion + ".json",
			"LICENSES/Apache-2.0.txt",
		},
		filepath.Join("..", "..", "..", "REUSE.toml"): {
			`path = "internal/engine/gate/conftest_capabilities.json"`,
			`SPDX-License-Identifier = "Apache-2.0"`,
		},
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(content), w) {
				t.Errorf("%s doesn't name %q", path, w)
			}
		}
	}
	if _, err := os.Stat(filepath.Join("..", "..", "..", "LICENSES", "Apache-2.0.txt")); err != nil {
		t.Error(err)
	}
}
