// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// The staging directory is always its own directory under the output's
// checkpoints, as engine.Add empties it: a root outside the output
// (--layout need not put it there) gets one named after a hash.
func TestStagingDirStaysUnderTheOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	base := filepath.Join(out, CheckpointDir, "incremental")

	if got, want := stagingDir(out, filepath.Join(out, "aws", "123")), filepath.Join(base, "aws", "123"); got != want {
		t.Errorf("a root in the output: got %s, want %s", got, want)
	}
	for _, dir := range []string{
		filepath.Join(filepath.Dir(out), "elsewhere", "aws"),
		filepath.Dir(out),
		out,
	} {
		got := stagingDir(out, dir)
		rel, err := filepath.Rel(base, got)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) || len(rel) != 16 {
			t.Errorf("%s: staging %s isn't a directory of its own under %s", dir, got, base)
		}
		if got != stagingDir(out, dir) {
			t.Errorf("%s: the staging directory changes between calls", dir)
		}
	}
}

// An error before the import knows which resources are new fails them all.
func TestAddToRootFailsEveryImportOnACheckpointError(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123")
	path := checkpointPath(out, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Run{options: Options{Out: out}}
	imports := []rootfile.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}, {Type: "aws_subnet", Name: "a", ID: "subnet-1"}}

	failed, result, holds, err := r.addToRoot(context.Background(), dir, imports, engine.Options{}, terraformRun{})

	if err == nil || result != nil || holds != nil {
		t.Fatalf("want the checkpoint error, got result=%v holds=%v err=%v", result, holds, err)
	}
	if !reflect.DeepEqual(failed, imports) {
		t.Errorf("failed: got %v, want every import", failed)
	}
}

// A resource the import that generated the root left out because its
// tags say another tool manages it is left out again, for that reason,
// rather than generated again only to be left out: its tags may be ones
// only Terraform reads, such as an IAM role's. With nothing else new, the
// root stays as it is.
func TestAddToRootLeavesOutWhatTheRootLeftOutAsOwned(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123", "global")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rootfile.GeneratedFileName), []byte("resource \"aws_iam_role\" \"app\" {\n  name = \"app\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rootfile.ImportsFileName), []byte("import {\n  to = aws_iam_role.app\n  id = \"app\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	owner := engine.Owner{Tool: "Terraform", Tag: "ManagedBy=Terraform"}
	previous := &engine.Result{
		Imported: []rootfile.Import{{Type: "aws_iam_role", Name: "app", ID: "app"}},
		Owned:    []engine.OwnedResource{{Address: "aws_iam_role.ci", ID: "ci", Owner: owner}},
	}
	if err := saveCheckpoint(out, dir, "fp", previous); err != nil {
		t.Fatal(err)
	}
	r := &Run{options: Options{Out: out}, scope: "aws/123/global"}
	imports := []rootfile.Import{{Type: "aws_iam_role", Name: "app", ID: "app"}, {Type: "aws_iam_role", Name: "ci", ID: "ci"}}

	added, result, holds, err := r.addToRoot(context.Background(), dir, imports, engine.Options{}, terraformRun{})

	if err != nil || added != nil || result != nil || holds != nil {
		t.Fatalf("want nothing new, got added=%v result=%v holds=%v err=%v", added, result, holds, err)
	}
	want := []report.Excluded{
		{Type: "aws_iam_role", ID: "app", Reason: whyText(report.InRoot()), Scope: "aws/123/global", Owner: report.OwnerTerraformRoot},
		{Type: "aws_iam_role", ID: "ci", Reason: selection.OwnedReason(owner), Scope: "aws/123/global", Owner: report.OwnerTerraformTag},
	}
	if !reflect.DeepEqual(r.report.Excluded, want) {
		t.Errorf("excluded:\ngot  %+v\nwant %+v", r.report.Excluded, want)
	}
}

// What the root left out as owned is generated after all when the import
// keeps owned resources, all of them or the one.
func TestPreviouslyOwned(t *testing.T) {
	owner := engine.Owner{Tool: "Pulumi", Tag: "pulumi:project=web"}
	previous := &engine.Result{Owned: []engine.OwnedResource{
		{Address: "aws_iam_role.ci", ID: "ci", Owner: owner},
		{Address: "aws_iam_user.deploy", ID: "deploy", Owner: owner},
	}}

	if got, want := previouslyOwned(previous, engine.Options{}), map[engine.External]engine.Owner{
		{Type: "aws_iam_role", ID: "ci"}:     owner,
		{Type: "aws_iam_user", ID: "deploy"}: owner,
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := previouslyOwned(previous, engine.Options{IncludeOwned: true}); len(got) != 0 {
		t.Errorf("--include-owned: got %v, want none", got)
	}
	got := previouslyOwned(previous, engine.Options{KeepOwned: map[string]bool{"aws_iam_role ci": true}})
	if _, ok := got[engine.External{Type: "aws_iam_role", ID: "ci"}]; ok || len(got) != 1 {
		t.Errorf("keeping aws_iam_role ci: got %v, want only the user", got)
	}
	if got := previouslyOwned(nil, engine.Options{}); got != nil {
		t.Errorf("no checkpoint: got %v, want none", got)
	}
}

// A root whose import didn't finish isn't added to: its generation
// started and has no result, or, without a checkpoint, it has Terraform's
// generated.tf but no README. It is refused, so the root is reported.
func TestDecideRootRefusesAnUnfinishedRoot(t *testing.T) {
	write := func(dir string, names ...string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			content := []byte("resource \"aws_vpc\" \"main\" {}\n")
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	out := t.TempDir()
	started := filepath.Join(out, "aws", "111")
	write(started, rootfile.ImportsFileName, rootfile.GeneratedFileName, rootfile.ReadmeFileName)
	if err := saveCheckpoint(out, started, "fp", nil); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(out, "aws", "222")
	write(raw, rootfile.ImportsFileName, rootfile.GeneratedFileName)

	c := &listedCall{options: Options{Out: out, Incremental: true}}
	for _, dir := range []string{started, raw} {
		d, err := c.decideRoot(dir, "fp")
		if err != nil || d.mode != rootRefused || d.reason == nil || !strings.Contains(d.reason.Error(), "didn't finish") {
			t.Errorf("%s: want an unfinished root refused, got mode %d, reason %v, err %v", dir, d.mode, d.reason, err)
		}
	}

	// A root generated in full, or with its README, is complete.
	done := filepath.Join(out, "aws", "333")
	write(done, rootfile.ImportsFileName, "network.tf")
	if err := saveCheckpoint(out, done, "fp", &engine.Result{Files: []string{"network.tf"}}); err != nil {
		t.Fatal(err)
	}
	withReadme := filepath.Join(out, "aws", "444")
	write(withReadme, rootfile.ImportsFileName, rootfile.GeneratedFileName, rootfile.ReadmeFileName)
	for _, dir := range []string{done, withReadme} {
		if unfinished, err := unfinishedRoot(out, dir); err != nil || unfinished {
			t.Errorf("%s: unfinished=%v, %v; want a complete root", dir, unfinished, err)
		}
	}
}

// An incremental import leaves out what another root of the output
// imports, naming that root: a subnetwork an earlier import put in its
// region's root isn't imported again into the global root, where imports
// now put subnetworks. The roots being imported, a root whose import
// blocks are gone but with no configuration left either, and what a root
// left out don't count, and a root left with nothing to import is
// dropped.
func TestIncrementalLeavesOutWhatAnotherRootImports(t *testing.T) {
	out := t.TempDir()
	global := filepath.Join(out, "google", "acme", "global")
	regional := filepath.Join(out, "google", "acme", "us-central1")
	gone := filepath.Join(out, "google", "acme", "europe-west1")
	network := rootfile.Import{Type: "google_compute_network", Name: "main", ID: "projects/acme/global/networks/main"}
	subnet := rootfile.Import{Type: "google_compute_subnetwork", Name: "app", ID: "projects/acme/regions/us-central1/subnetworks/app"}
	rejected := rootfile.Import{Type: "google_compute_subnetwork", Name: "bad", ID: "projects/acme/regions/us-central1/subnetworks/bad"}
	eu := rootfile.Import{Type: "google_compute_subnetwork", Name: "eu", ID: "projects/acme/regions/europe-west1/subnetworks/eu"}
	for dir, result := range map[string]*engine.Result{
		global:   {Imported: []rootfile.Import{network}},
		regional: {Imported: []rootfile.Import{subnet}, Rejected: []engine.Rejection{{Address: "google_compute_subnetwork.bad", ID: rejected.ID}}},
		gone:     {Imported: []rootfile.Import{eu}},
	} {
		if err := saveCheckpoint(out, dir, "fp", result); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{global, regional} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rootfile.ImportsFileName), []byte("# imports\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	byDir := map[string][]rootfile.Import{global: {network, subnet, rejected, eu}}

	held, err := heldElsewhere(out, byDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[engine.External]string{{Type: subnet.Type, ID: subnet.ID}: "google/acme/us-central1"}; !reflect.DeepEqual(held, want) {
		t.Fatalf("held elsewhere: got %v, want %v", held, want)
	}
	r := &Run{options: Options{Out: out}, scope: "acme/global"}
	r.leaveOutHeld(byDir, held)
	if want := []rootfile.Import{network, rejected, eu}; !reflect.DeepEqual(byDir[global], want) {
		t.Errorf("global: got %v, want %v", byDir[global], want)
	}
	want := []report.Excluded{{Type: subnet.Type, ID: subnet.ID, Reason: whyText(report.InOtherRoot("google/acme/us-central1")), Scope: "acme/global", Owner: report.OwnerTerraformRoot}}
	if !reflect.DeepEqual(r.report.Excluded, want) {
		t.Errorf("excluded:\ngot  %+v\nwant %+v", r.report.Excluded, want)
	}

	only := map[string][]rootfile.Import{global: {subnet}}
	r.leaveOutHeld(only, held)
	if _, ok := only[global]; ok {
		t.Errorf("a root with nothing left to import is kept: %v", only)
	}
}

// With no checkpoints, as before an output's first import, nothing is
// held elsewhere.
func TestHeldElsewhereWithoutCheckpoints(t *testing.T) {
	out := t.TempDir()
	held, err := heldElsewhere(out, map[string][]rootfile.Import{filepath.Join(out, "aws", "123"): nil})
	if err != nil || len(held) != 0 {
		t.Errorf("got %v, %v; want nothing", held, err)
	}
}

// A root applied since, its import blocks deleted, still holds what its
// checkpoint records.
func TestHeldElsewhereCountsAnAppliedRoot(t *testing.T) {
	out := t.TempDir()
	applied, next := filepath.Join(out, "aws", "applied"), filepath.Join(out, "aws", "next")
	queue := rootfile.Import{Type: "aws_sqs_queue", Name: "orders", ID: "https://sqs/orders"}
	if err := os.MkdirAll(applied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(applied, rootfile.GeneratedFileName), []byte("# generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := &engine.Result{Imported: []rootfile.Import{queue}, Addresses: []engine.Managed{{Type: queue.Type, ID: queue.ID, Address: "aws_sqs_queue.orders"}}}
	if err := saveCheckpoint(out, applied, "fp", result); err != nil {
		t.Fatal(err)
	}
	held, err := heldElsewhere(out, map[string][]rootfile.Import{next: {queue}})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[engine.External]string{{Type: queue.Type, ID: queue.ID}: "aws/applied"}; !reflect.DeepEqual(held, want) {
		t.Errorf("held: got %v, want %v", held, want)
	}
}
