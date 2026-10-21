// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path"
	"path/filepath"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// bucketModuleDir is where bucketCalls call the bucket module from.
const bucketModuleDir = "../../../modules/s3_0a1b2c3d"

// withBucketModule returns files, a root's, with the bucket module's
// files, as drift edits them (see addModuleFiles).
func withBucketModule(files map[string]string) map[string][]byte {
	contents := byteFiles(files)
	for name, content := range bucketModule {
		contents[path.Join(bucketModuleDir, name)] = []byte(content)
	}
	return contents
}

// A literal of a generated module that calls or environments share, that
// one of them changed in the cloud, becomes a variable of the module whose
// default is the literal, which the call that changed sets: to the
// cloud's value, or, in a root environments share, to a variable of the
// root that the environment's var file sets.
func TestGoldenDriftPromotedLiterals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, env string
		files     map[string]string
		calls     int
	}{
		{name: "calls", files: map[string]string{"main.tf": bucketCalls}, calls: 2},
		{name: "environments", env: "dev", calls: 1, files: map[string]string{
			"main.tf":                   strings.SplitN(bucketCalls, "\nmodule \"data\"", 2)[0],
			"variables.tf":              "variable \"env\" {\n  description = \"The token that stands for the environment in names.\"\n  type        = string\n}\n",
			"vars/dev/terraform.tfvars": "env = \"dev\"\n",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			change := moduleDrift("module.logs.aws_s3_bucket.this", "aws_s3_bucket", "force_destroy", true)
			e := testEditor(map[string]map[string]any{change.Address: {"bucket": "logs", "force_destroy": true}})
			m := testModule(t, bucketModuleDir, tc.calls, bucketModule)
			e.modules = map[string]*driftModule{"logs": m, "data": m}
			if tc.env != "" {
				e.env, e.envVars = tc.env, envVarsPath(tc.env)
			}
			contents := withBucketModule(tc.files)
			names, applied, failed, err := e.apply(contents, []*DriftChange{change})
			if err != nil {
				t.Fatal(err)
			}
			src, ok := applied[change]
			if !ok {
				t.Fatalf("no edit: %s", failed[change].Reason)
			}
			if src.File != "main.tf" || src.Kind != SourceModule {
				t.Errorf("source %+v", src)
			}
			edited := map[string][]byte{}
			for _, name := range names {
				edited[name] = contents[name]
			}
			goldenFiles(t, filepath.Join("testdata", "driftpromote", tc.name+".golden.txt"), edited)
		})
	}
}

// A literal is a hand edit, as it was, in a module that isn't generated,
// in one no other call or environment shares, or where the module's
// files aren't at hand.
func TestDriftKeepsLiteralsOfOtherModules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, dir, reason string
		calls             int
		files             map[string][]byte
	}{
		{"a registry module", ".terraform/modules/logs", "a literal that its 2 calls share", 2, withBucketModule(map[string]string{"main.tf": bucketCalls})},
		{"one call", bucketModuleDir, "sets it to a literal: edit it there by hand", 1, withBucketModule(map[string]string{"main.tf": bucketCalls})},
		{"without the module's files", bucketModuleDir, "a literal that its 2 calls share", 2, byteFiles(map[string]string{"main.tf": bucketCalls})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			change := moduleDrift("module.logs.aws_s3_bucket.this", "aws_s3_bucket", "force_destroy", true)
			e := testEditor(map[string]map[string]any{change.Address: {"force_destroy": true}})
			m := testModule(t, tc.dir, tc.calls, bucketModule)
			e.modules = map[string]*driftModule{"logs": m, "data": m}
			_, applied, failed, err := e.apply(tc.files, []*DriftChange{change})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := applied[change]; ok {
				t.Fatal("edited")
			}
			if reason := failed[change].Reason; !strings.Contains(reason, tc.reason) {
				t.Errorf("reason %q, want one saying %q", reason, tc.reason)
			}
		})
	}
}

// Accepted, the change is planned with the module's copy in the staged
// root edited, then written into the module and the call.
func TestDriftAcceptsByPromotingALiteral(t *testing.T) {
	t.Parallel()
	dir, staging := driftModuleRoot(t, map[string]string{"main.tf": bucketCalls}, map[string]map[string]string{"s3_0a1b2c3d": bucketModule}, map[string]string{"logs": "s3_0a1b2c3d", "data": "s3_0a1b2c3d"})
	update, noop := tfjson.Actions{tfjson.ActionUpdate}, tfjson.Actions{tfjson.ActionNoop}
	bucket := func(address string, actions tfjson.Actions, from, to bool) *tfjson.ResourceChange {
		return driftResource(address, "aws_s3_bucket", actions, map[string]any{"force_destroy": from}, map[string]any{"force_destroy": to})
	}
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{bucket("module.logs.aws_s3_bucket.this", update, false, true)}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		bucket("module.logs.aws_s3_bucket.this", update, true, false),
		bucket("module.data.aws_s3_bucket.this", noop, false, false),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		bucket("module.logs.aws_s3_bucket.this", noop, true, true),
		bucket("module.data.aws_s3_bucket.this", noop, false, false),
	}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c := outcomes(result)["module.logs.aws_s3_bucket.this force_destroy"]; c.Outcome != DriftWritten {
		t.Fatalf("%s: %s", c.Outcome, c.Reason)
	}
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	module := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(dir))), "modules", "s3_0a1b2c3d")
	if got := readFile(t, module, "main.tf"); !strings.Contains(flat(got), "force_destroy = var.force_destroy") {
		t.Errorf("the module's main.tf:\n%s", got)
	}
	if got := readFile(t, module, "variables.tf"); !strings.Contains(flat(got), `variable "force_destroy" {`) || !strings.Contains(flat(got), "default = false") {
		t.Errorf("the module's variables.tf:\n%s", got)
	}
	main := readFile(t, dir, "main.tf")
	logs, data, _ := strings.Cut(main, "module \"data\"")
	if !strings.Contains(flat(logs), "force_destroy = true") || strings.Contains(data, "force_destroy") {
		t.Errorf("main.tf:\n%s", main)
	}
}
