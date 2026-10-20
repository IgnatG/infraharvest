// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// driftAccount is the account testdata/aws-drift creates its resources
// in, which no other test uses.
const driftAccount = "666666666666"

// driftProfile is the profile the test imports driftAccount as.
const driftProfile = "infraharvest-e2e-drift"

// TestAWSDrift imports testdata/aws-drift, applies the roots into local
// states, as a team would, and changes the resources out of band, by
// applying the fixture again with other variables. It then checks what
// infraharvest drift reports and writes:
//
//   - Report only: a queue's tag and retention are kept, with
//     --detailed-exitcode exiting 4, and the code isn't changed.
//   - --accept and --keep: the last rule that matches a change decides it.
//   - The decisions file: --write-decisions writes the retention's entry,
//     undecided; set to accept, it is kept and marked decision_stale once
//     the retention changes again, and, set to accept again, written.
//   - The tag every resource gets from the provider's default tags,
//     changed on all of them: one change, of local.tags, accepted there.
//   - The tag of the bucket, a call of the curated s3-bucket module:
//     accepted into the call's tags, with source.module_path.
//
// The code with every change accepted then plans no change, and, once the
// state is refreshed, drift finds nothing.
//
// Imported again with a root per service ({service} in --layout) and
// applied, the queue's and the bucket's tags change in the cloud: drift
// with --parallel=3 checks both roots at once, and with --format=json
// prints drift.json as it writes it; it finds each root's change, and,
// with --accept, writes each into its root, which then plans no change.
func TestAWSDrift(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	fixture := seedApplied(ctx, t, "aws-drift", execPath, plugins)
	vars := map[string]string{"project": "infraharvest-e2e-drift", "team": "web", "retention": "86400", "bucket_team": "data"}
	change := func(values map[string]string) {
		t.Helper()
		var opts []tfexec.ApplyOption
		for k, v := range values {
			vars[k] = v
		}
		for k, v := range vars {
			opts = append(opts, tfexec.Var(k+"="+v))
		}
		fixture.apply(ctx, t, opts...)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", driftAccount)
	// The import keeps the credentials it resolves for the rest of the
	// process, by profile: a profile of its own keeps the tests after it
	// out of this account.
	credentials := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(credentials, []byte("[default]\naws_access_key_id = test\naws_secret_access_key = test\n\n["+driftProfile+"]\naws_access_key_id = "+driftAccount+"\naws_secret_access_key = test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentials)

	out := t.TempDir()
	runMovesImport(ctx, t,
		"import", "aws",
		"--engine="+engineName,
		"--terraform-path="+execPath,
		"--profile="+driftProfile,
		"--regions=us-east-1",
		"--services=sqs,s3",
		"--out="+out,
		"--all",
	)
	checkCoverage(t, "the import", coverageOf(t, out), nil)
	dirs := applyGenerated(ctx, t, out, execPath, plugins)
	drift := driftRun{ctx: ctx, engineName: engineName, execPath: execPath, out: out}

	// Report only.
	change(map[string]string{"team": "ops", "retention": "172800"})
	code := codeFiles(t, out)
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode")
	if !d.DriftFound || d.Totals.Kept < 2 || d.Totals.Written != 0 {
		t.Errorf("report only: drift_found %v, totals %+v", d.DriftFound, d.Totals)
	}
	team := driftChange(t, "report only", d, "aws_sqs_queue", "tags.team")
	checkDriftChange(t, "report only", team, "web", "ops", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	retention := driftChange(t, "report only", d, "aws_sqs_queue", "message_retention_seconds")
	checkDriftChange(t, "report only", retention, "86400", "172800", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if retention.Source == nil || !strings.HasSuffix(retention.Source.File, ".tf") || retention.Source.Line == 0 {
		t.Errorf("report only: %s %s has no source in the code: %+v", retention.Address, retention.Attribute, retention.Source)
	}
	md := readFile(t, filepath.Join(out, report.Dir, report.DriftMarkdownFile))
	for _, want := range []string{retention.Address, "message_retention_seconds", "tags.team"} {
		if !strings.Contains(md, want) {
			t.Errorf("%s doesn't name %s:\n%s", report.DriftMarkdownFile, want, md)
		}
	}
	checkCodeUnchanged(t, "report only", code, out)

	// The last rule that matches a change decides it.
	d = drift.mustExit(t, report.ExitOK, "--accept", "attribute=tags.team", "--keep", "type=aws_sqs_queue")
	checkDriftChange(t, "--accept then --keep", driftChange(t, "--accept then --keep", d, "aws_sqs_queue", "tags.team"), "web", "ops", report.DriftKeep, report.DriftDecidedByFlag, report.DriftKept)
	checkDriftChange(t, "--accept then --keep", driftChange(t, "--accept then --keep", d, "aws_sqs_queue", "message_retention_seconds"), "86400", "172800", report.DriftKeep, report.DriftDecidedByFlag, report.DriftKept)
	checkCodeUnchanged(t, "--accept then --keep", code, out)
	d = drift.mustExit(t, report.ExitOK, "--keep", "all", "--accept", "attribute=tags.team")
	checkDriftChange(t, "--keep then --accept", driftChange(t, "--keep then --accept", d, "aws_sqs_queue", "tags.team"), "web", "ops", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	checkDriftChange(t, "--keep then --accept", driftChange(t, "--keep then --accept", d, "aws_sqs_queue", "message_retention_seconds"), "86400", "172800", report.DriftKeep, report.DriftDecidedByFlag, report.DriftKept)
	if !codeMatches(t, out, `team\s*=\s*"ops"`) || codeMatches(t, out, `team\s*=\s*"web"`) {
		t.Errorf("--keep then --accept: the code doesn't have the queue's tag team = \"ops\" in place of \"web\"")
	}

	// The decisions file. The tag accepted above is absorbed now: the
	// code has the cloud's value, and drift doesn't write the state.
	decisions := filepath.Join(out, importer.DefaultDriftDecisionsFile)
	code = codeFiles(t, out)
	d = drift.mustExit(t, report.ExitOK, "--write-decisions")
	if c := driftChange(t, "--write-decisions", d, "aws_sqs_queue", "tags.team"); c.Outcome != report.DriftAbsorbed {
		t.Errorf("--write-decisions: the accepted tag is %s, want %s", c.Outcome, report.DriftAbsorbed)
	}
	checkCodeUnchanged(t, "--write-decisions", code, out)
	entry := decisionEntry(t, decisions, "message_retention_seconds", importer.DriftUndecided)
	for _, e := range loadDecisions(t, decisions).Changes {
		if e.Attribute == "tags.team" {
			t.Errorf("%s has an entry for the absorbed tag: %+v", decisions, e)
		}
	}
	setDecision(t, decisions, "message_retention_seconds", report.DriftAccept)
	// Changed again in the cloud, the entry holds for other values.
	change(map[string]string{"retention": "259200"})
	d = drift.mustExit(t, report.ExitDrift, "--decisions", decisions, "--write-decisions", "--detailed-exitcode")
	stale := driftChange(t, "a stale decision", d, "aws_sqs_queue", "message_retention_seconds")
	checkDriftChange(t, "a stale decision", stale, "86400", "259200", report.DriftKeep, report.DriftDecidedByEntry, report.DriftKept)
	if !stale.DecisionStale {
		t.Errorf("a stale decision: %s %s isn't decision_stale", stale.Address, stale.Attribute)
	}
	if !strings.Contains(readFile(t, filepath.Join(out, report.Dir, report.DriftMarkdownFile)), stale.Address) {
		t.Errorf("a stale decision: %s doesn't name %s", report.DriftMarkdownFile, stale.Address)
	}
	checkCodeUnchanged(t, "a stale decision", code, out)
	if again := decisionEntry(t, decisions, "message_retention_seconds", importer.DriftUndecided); again.Fingerprint == entry.Fingerprint {
		t.Errorf("--write-decisions kept the fingerprint %s of the values decided before", entry.Fingerprint)
	}
	setDecision(t, decisions, "message_retention_seconds", report.DriftAccept)
	d = drift.mustExit(t, report.ExitOK, "--decisions", decisions)
	decided := driftChange(t, "--decisions", d, "aws_sqs_queue", "message_retention_seconds")
	checkDriftChange(t, "--decisions", decided, "86400", "259200", report.DriftAccept, report.DriftDecidedByEntry, report.DriftWritten)
	if decided.DecisionStale {
		t.Errorf("--decisions: %s %s is decision_stale", decided.Address, decided.Attribute)
	}
	if !codeMatches(t, out, `message_retention_seconds\s*=\s*259200`) {
		t.Errorf("--decisions: the code doesn't set message_retention_seconds = 259200")
	}

	// The tag every resource shares, and the bucket's own tag, in a call
	// of the s3-bucket module.
	change(map[string]string{"project": "infraharvest-e2e-drift-v2", "bucket_team": "ops"})
	code = codeFiles(t, out)
	d = drift.mustExit(t, report.ExitDrift, "--detailed-exitcode")
	checkCodeUnchanged(t, "shared tags", code, out)
	shared := driftChange(t, "shared tags", d, "", "tags.Project")
	checkDriftChange(t, "shared tags", shared, "infraharvest-e2e-drift", "infraharvest-e2e-drift-v2", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if !strings.HasPrefix(shared.Address, "local.") && !strings.HasPrefix(shared.Address, "provider.") {
		t.Errorf("shared tags: the change is of %s, want the place the root keeps them", shared.Address)
	}
	queues := 0
	for _, address := range shared.Covers {
		if strings.Contains(address, "aws_sqs_queue.") {
			queues++
		}
	}
	if queues != 2 {
		t.Errorf("shared tags: %s covers %v, want both queues", shared.Address, shared.Covers)
	}
	for _, c := range d.Changes {
		if c.Attribute == "tags.Project" && len(c.Covers) == 0 && strings.HasPrefix(c.Type, "aws_sqs_queue") {
			t.Errorf("shared tags: %s's tag is a change of its own as well: %+v", c.Address, c)
		}
	}
	bucket := driftChange(t, "a module call", d, "aws_s3_bucket", "tags.team")
	checkDriftChange(t, "a module call", bucket, "data", "ops", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if bucket.ModuleCall == "" || bucket.Source == nil || bucket.Source.ModulePath == "" {
		t.Errorf("a module call: %s isn't traced through its module call %q: %+v", bucket.Address, bucket.ModuleCall, bucket.Source)
	}
	d = drift.mustExit(t, report.ExitOK, "--accept", "attribute=tags.*", "--detailed-exitcode")
	checkDriftChange(t, "shared tags accepted", driftChange(t, "shared tags accepted", d, "", "tags.Project"), "infraharvest-e2e-drift", "infraharvest-e2e-drift-v2", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	checkDriftChange(t, "a module call accepted", driftChange(t, "a module call accepted", d, "aws_s3_bucket", "tags.team"), "data", "ops", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if !codeMatches(t, out, `"infraharvest-e2e-drift-v2"`) || codeMatches(t, out, `"infraharvest-e2e-drift"`) {
		t.Errorf("shared tags accepted: the code doesn't have the new Project tag in place of the old")
	}
	if codeMatches(t, out, `team\s*=\s*"data"`) {
		t.Errorf("a module call accepted: the code still has the bucket's tag team = \"data\"")
	}

	checkAccepted(ctx, t, dirs, execPath, plugins)
	if d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode"); len(d.Changes) != 0 {
		t.Errorf("with the state refreshed: changes\n%s", driftChanges(d))
	}

	// A root per service, checked at once (--parallel), with the report
	// printed (--format=json): each root's change is found and accepted
	// as it is checked alone.
	split := t.TempDir()
	runMovesImport(ctx, t,
		"import", "aws",
		"--engine="+engineName,
		"--terraform-path="+execPath,
		"--profile="+driftProfile,
		"--regions=us-east-1",
		"--services=sqs,s3",
		"--layout={out}/{provider}/{account}/{region}/{service}/",
		"--out="+split,
		"--all",
	)
	checkCoverage(t, "the import by service", coverageOf(t, split), nil)
	splitDirs := applyGenerated(ctx, t, split, execPath, plugins)
	if len(splitDirs) < 2 {
		t.Fatalf("the import by service generated %v, want a root for sqs and one for s3", splitDirs)
	}
	change(map[string]string{"team": "qa", "bucket_team": "qa"})
	drift = driftRun{ctx: ctx, engineName: engineName, execPath: execPath, out: split}
	code = codeFiles(t, split)
	d = drift.mustExitJSON(t, report.ExitDrift, "--parallel=3", "--detailed-exitcode")
	checkCodeUnchanged(t, "--parallel", code, split)
	if len(d.Roots) != len(splitDirs) || d.Totals.Failed != 0 {
		t.Errorf("--parallel: roots %+v, totals %+v", d.Roots, d.Totals)
	}
	queue, bucket := driftChange(t, "--parallel", d, "aws_sqs_queue", "tags.team"), driftChange(t, "--parallel", d, "aws_s3_bucket", "tags.team")
	checkDriftChange(t, "--parallel", queue, "ops", "qa", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	checkDriftChange(t, "--parallel", bucket, "ops", "qa", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if queue.Root == bucket.Root {
		t.Errorf("--parallel: the queue and the bucket are both in %s", queue.Root)
	}
	d = drift.mustExitJSON(t, report.ExitOK, "--parallel=3", "--accept", "attribute=tags.team", "--detailed-exitcode")
	checkDriftChange(t, "--parallel, accepted", driftChange(t, "--parallel, accepted", d, "aws_sqs_queue", "tags.team"), "ops", "qa", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	checkDriftChange(t, "--parallel, accepted", driftChange(t, "--parallel, accepted", d, "aws_s3_bucket", "tags.team"), "ops", "qa", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if !codeMatches(t, split, `team\s*=\s*"qa"`) || codeMatches(t, split, `team\s*=\s*"ops"`) {
		t.Errorf("--parallel, accepted: the code doesn't have team = \"qa\" in place of \"ops\"")
	}
	checkAccepted(ctx, t, splitDirs, execPath, plugins)
}

// loadDecisions reads the decisions file at path.
func loadDecisions(t *testing.T, path string) *importer.DriftDecisions {
	t.Helper()
	f, err := importer.LoadDriftDecisions(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// decisionEntry returns the entry of the decisions file at path for a
// change to attribute, failing the test unless there is one, decided as
// decision.
func decisionEntry(t *testing.T, path, attribute, decision string) importer.DriftDecisionEntry {
	t.Helper()
	f := loadDecisions(t, path)
	for _, e := range f.Changes {
		if e.Attribute == attribute {
			if e.Decision != decision || e.Fingerprint == "" {
				t.Errorf("%s: the entry of %s %s is %s with fingerprint %q, want %s", path, e.Address, attribute, e.Decision, e.Fingerprint, decision)
			}
			return e
		}
	}
	t.Fatalf("%s has no entry for %s: %+v", path, attribute, f.Changes)
	return importer.DriftDecisionEntry{}
}

// setDecision sets the decision of the entries of the decisions file at
// path for changes to attribute, as a person reviewing it does.
func setDecision(t *testing.T, path, attribute, decision string) {
	t.Helper()
	f := loadDecisions(t, path)
	for i := range f.Changes {
		if f.Changes[i].Attribute == attribute {
			f.Changes[i].Decision = decision
		}
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
}
