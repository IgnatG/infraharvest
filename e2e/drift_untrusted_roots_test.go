// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/report"
)

// driftUntrustedAccount is the account TestAWSDriftUntrustedRoots creates
// testdata/aws-drift-queues in, which no other test uses.
const driftUntrustedAccount = "838383838383"

// driftUntrustedProfile is the profile the test imports
// driftUntrustedAccount as.
const driftUntrustedProfile = "infraharvest-e2e-drift-untrusted"

// TestAWSDriftUntrustedRoots imports testdata/aws-drift-queues, applies
// the root into a local state, and changes a queue's tag out of band. The
// root then carries what a root from someone else could: a provider in its
// .terraform directory that is a script, and a filesystem mirror, a
// terraform.d/plugins directory, with a script as the provider version its
// lock file pins; each records its run in a file outside the root.
// infraharvest drift --untrusted-roots must ignore both, as it installs
// the providers from their registry alone and keeps its working files out
// of the root: it reports the change, kept, exiting 4 with
// --detailed-exitcode, and, with --accept, writes it into the code, as it
// does without the flag; neither script runs, and nothing under the root's
// .terraform changes. With the scripts removed, the code plans no change.
func TestAWSDriftUntrustedRoots(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := pluginCacheDir(cache)
	account := tfexec.Var("account=" + driftUntrustedAccount)
	fixture := seedApplied(ctx, t, "aws-drift-queues", execPath, plugins, account)
	useAWSAccount(t, driftUntrustedAccount, driftUntrustedProfile)

	out := t.TempDir()
	runMovesImport(ctx, t,
		"import", "aws",
		"--engine="+engineName,
		"--terraform-path="+execPath,
		"--profile="+driftUntrustedProfile,
		"--regions=us-east-1",
		"--services=sqs",
		"--out="+out,
		"--all",
	)
	checkCoverage(t, "the import", coverageOf(t, out), nil)
	dirs := applyGenerated(ctx, t, out, execPath, plugins)
	fixture.apply(ctx, t, account, tfexec.Var("team=ops"))
	drift := driftRun{ctx: ctx, engineName: engineName, execPath: execPath, out: out}

	ran := filepath.Join(t.TempDir(), "ran")
	var restore []func()
	for _, dir := range dirs {
		restore = append(restore, plantProviders(t, dir, ran, binary.Registry)...)
	}
	restored := false
	restoreAll := func() {
		if restored {
			return
		}
		restored = true
		for i := len(restore) - 1; i >= 0; i-- {
			restore[i]()
		}
	}
	t.Cleanup(restoreAll)
	before := map[string]string{}
	for _, dir := range dirs {
		before[dir] = terraformDirSnapshot(t, dir)
	}
	checkUntouched := func(what string) {
		t.Helper()
		if content, err := os.ReadFile(ran); err == nil {
			t.Errorf("%s: a provider of the root ran:\n%s", what, content)
		}
		for _, dir := range dirs {
			if after := terraformDirSnapshot(t, dir); after != before[dir] {
				t.Errorf("%s: the .terraform of %s changed:\n%s\nwas\n%s", what, dir, after, before[dir])
			}
		}
	}

	code := codeFiles(t, out)
	d := drift.mustExit(t, report.ExitDrift, "--untrusted-roots", "--detailed-exitcode")
	if !d.DriftFound || d.Totals.Kept == 0 || d.Totals.Written != 0 || len(d.Roots) != len(dirs) {
		t.Errorf("report only: drift_found %v, totals %+v, roots %+v", d.DriftFound, d.Totals, d.Roots)
	}
	checkDriftChange(t, "report only", driftChange(t, "report only", d, "aws_sqs_queue", "tags.team"), "web", "ops", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	checkCodeUnchanged(t, "report only", code, out)
	checkUntouched("report only")

	d = drift.mustExit(t, report.ExitOK, "--untrusted-roots", "--accept", "attribute=tags.team")
	checkDriftChange(t, "accepted", driftChange(t, "accepted", d, "aws_sqs_queue", "tags.team"), "web", "ops", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if !codeMatches(t, out, `team\s*=\s*"ops"`) || codeMatches(t, out, `team\s*=\s*"web"`) {
		t.Errorf("accepted: the code doesn't have the queue's tag team = \"ops\" in place of \"web\"")
	}
	checkUntouched("accepted")

	restoreAll()
	checkAccepted(ctx, t, dirs, execPath, plugins)
	if d = drift.mustExit(t, report.ExitOK, "--untrusted-roots", "--detailed-exitcode"); len(d.Changes) != 0 {
		t.Errorf("with the state refreshed: changes\n%s", driftChanges(d))
	}
}

// plantProviders makes dir carry what a root from someone else could, as
// scripts that append their path to ran and fail: each aws provider
// Terraform installed in dir's .terraform directory is replaced, and a
// filesystem mirror, dir/terraform.d/plugins, has one of the version the
// root's lock file pins. registry is the host the engine installs from
// (binary.Registry: registry.terraform.io, or registry.opentofu.org for
// OpenTofu). It returns what undoes it, last first.
func plantProviders(t *testing.T, dir, ran, registry string) []func() {
	t.Helper()
	script := []byte("#!/bin/sh\necho \"$0\" >> '" + ran + "'\nexit 1\n")
	var undo []func()

	// Terraform links the platform directory of each provider into the
	// plugin cache, so it is the link that is replaced, never what it
	// points to.
	installed, err := filepath.Glob(filepath.Join(dir, ".terraform", "providers", registry, "hashicorp", "aws", "*", "*"))
	if err != nil || len(installed) == 0 {
		t.Fatalf("%s: no aws provider installed in .terraform: %v", dir, err)
	}
	for i, platform := range installed {
		kept := filepath.Join(dir, ".terraform", fmt.Sprintf("kept-%d", i))
		if err := os.Rename(platform, kept); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(platform, 0o755); err != nil {
			t.Fatal(err)
		}
		name := "terraform-provider-aws_v" + filepath.Base(filepath.Dir(platform)) + "_x5"
		if err := os.WriteFile(filepath.Join(platform, name), script, 0o755); err != nil {
			t.Fatal(err)
		}
		undo = append(undo, func() {
			if err := os.RemoveAll(platform); err != nil {
				t.Error(err)
			}
			if err := os.Rename(kept, platform); err != nil {
				t.Error(err)
			}
		})
	}

	lock := readFile(t, filepath.Join(dir, ".terraform.lock.hcl"))
	m := regexp.MustCompile(`provider "` + regexp.QuoteMeta(registry) + `/hashicorp/aws" \{\s*version\s*=\s*"([^"]+)"`).FindStringSubmatch(lock)
	if m == nil {
		t.Fatalf("%s: no aws version in the lock file:\n%s", dir, lock)
	}
	mirror := filepath.Join(dir, "terraform.d", "plugins", registry, "hashicorp", "aws", m[1], runtime.GOOS+"_"+runtime.GOARCH)
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mirror, "terraform-provider-aws_v"+m[1]+"_x5"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	undo = append(undo, func() {
		if err := os.RemoveAll(filepath.Join(dir, "terraform.d")); err != nil {
			t.Error(err)
		}
	})
	return undo
}

// terraformDirSnapshot returns what dir's .terraform directory has, one
// line per entry: its path, mode, size, modification time, and, for a
// link, where it points. A provider's content is too large to read, and a
// change of it shows in the others.
func terraformDirSnapshot(t *testing.T, dir string) string {
	t.Helper()
	root := filepath.Join(dir, ".terraform")
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(&b, "%s %v %d", filepath.ToSlash(rel), info.Mode(), info.Size())
		if !d.IsDir() {
			fmt.Fprintf(&b, " %d", info.ModTime().UnixNano())
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, " -> %s", target)
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
