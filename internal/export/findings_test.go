// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/report"
)

const findingsDir = "aws/123456789012/eu-west-2"

func decodeFindings(t *testing.T, content []byte) findingsDoc {
	t.Helper()
	var doc findingsDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// eventOf returns the event of the check id, or fails.
func findingOf(t *testing.T, doc findingsDoc, id string) findingEvent {
	t.Helper()
	for _, e := range doc.Events {
		if strings.HasPrefix(e.FindingInfo.Title, id+" ") {
			return e
		}
	}
	t.Fatalf("no event of %s in %+v", id, doc.Events)
	return findingEvent{}
}

// The failed check and the skipped one of the import are findings, as
// Detection Findings of the Findings category; the check that passed is not.
func TestFindingsOfAnImport(t *testing.T) {
	t.Parallel()
	out := writeOut(t)
	content := build(t, Options{Out: out, Document: DocOCSFFindings, RepoURL: ocsfRepo, CommitSHA: ocsfCommit, StateURL: func(string) string { return "s3://bucket/key.tfstate" }})
	if err := validate(DocOCSFFindings, content); err != nil {
		t.Fatal(err)
	}
	doc := decodeFindings(t, content)
	if doc.Schema != schemaOCSFFindings || doc.OCSFVersion != "1.9.0" || doc.RunID == "" {
		t.Errorf("document: %+v", doc)
	}
	if len(doc.Events) != 2 {
		t.Fatalf("%d events, want 2 (G5 and G6): %+v", len(doc.Events), doc.Events)
	}
	// The events are in the order of the checks.
	secrets, scanners := doc.Events[1], doc.Events[0]
	if scanners.FindingInfo.Title != "G5 scanners" || secrets.FindingInfo.Title != "G6 secrets" {
		t.Errorf("titles: %q, %q", scanners.FindingInfo.Title, secrets.FindingInfo.Title)
	}
	for _, e := range doc.Events {
		if e.ClassUID != 2004 || e.ClassName != "Detection Finding" || e.CategoryUID != 2 || e.CategoryName != "Findings" ||
			e.ActivityID != 1 || e.ActivityName != "Create" || e.TypeUID != 200401 || e.StatusID != 1 || e.Status != "New" {
			t.Errorf("%s: class fields: %+v", e.FindingInfo.Title, e)
		}
		if e.Time != fixedNow().UnixMilli() {
			t.Errorf("%s: time %d", e.FindingInfo.Title, e.Time)
		}
		if e.Metadata.Version != "1.9.0" || e.Metadata.CorrelationUID != doc.RunID || e.Metadata.Product.Name != "infraharvest" || e.Metadata.Product.Version != "v0.1.0" {
			t.Errorf("%s: metadata: %+v", e.FindingInfo.Title, e.Metadata)
		}
		if e.Cloud == nil || e.Cloud.Provider != "AWS" || e.Cloud.Account == nil || e.Cloud.Account.UID != "123456789012" || e.Cloud.Region != "eu-west-2" {
			t.Errorf("%s: cloud: %+v", e.FindingInfo.Title, e.Cloud)
		}
	}
	if secrets.SeverityID != 4 || secrets.Severity != "High" || !secrets.IsAlert || secrets.Message != "G6 secrets failed: secrets_found" {
		t.Errorf("secrets: %+v", secrets)
	}
	if want := []string{"secrets_found"}; !equalStrings(secrets.FindingInfo.Types, want) {
		t.Errorf("secrets types: %v", secrets.FindingInfo.Types)
	}
	if scanners.SeverityID != 2 || scanners.Severity != "Low" || scanners.IsAlert || scanners.Message != "G5 scanners warned: tool_missing" {
		t.Errorf("scanners: %+v", scanners)
	}
	if want := doc.RunID + ":G6:1"; secrets.FindingInfo.UID != want {
		t.Errorf("uid %q, want %q", secrets.FindingInfo.UID, want)
	}
	// The root's iac source, for the root the import generated.
	res := secrets.Resources
	if len(res) != 1 || res[0].Type != "iac_root" || res[0].UID != findingsDir {
		t.Fatalf("resources: %+v", res)
	}
	want := iacSource{Tool: "terraform", RepoURL: ocsfRepo, CommitSHA: ocsfCommit, Path: findingsDir, StateKey: "s3://bucket/key.tfstate", OutcomeID: 1, Outcome: importWritten}
	if res[0].IaC == nil || *res[0].IaC != want {
		t.Errorf("iac: %+v, want %+v", res[0].IaC, want)
	}
	if doc.Redaction != (findingsRedaction{HashAlg: hashAlg}) {
		t.Errorf("redaction: %+v", doc.Redaction)
	}
}

// The event holds the check's name and reason codes, and nothing of what a
// check says in its details.
func TestFindingsHoldNoDetails(t *testing.T) {
	t.Parallel()
	r := &report.Report{}
	r.Engine = report.Component{Name: "tofu", Version: "1.10.0"}
	r.Provider.Source = "hashicorp/google"
	r.Directories = []report.Directory{{
		Path: "google/p",
		Checks: []report.Check{
			{Name: "G3 plan", Passed: false, Details: []string{"google_storage_bucket.private-" + secretAttribute + " would be replaced"}},
			{Name: "G4 standards", Passed: false, Reason: gate.ReasonNotRun, Details: []string{"didn't run: " + secretAttribute}},
			{Name: "G2 validate", Passed: false, Details: []string{secretAttribute}},
			{Name: "not a gate check", Passed: false, Details: []string{secretAttribute}},
		},
	}}
	doc, err := buildFindingsOfReport(r, Options{}, "6f1c9d3e-0a52-5b8e-9c47-2d1f3a5b7c90", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), secretAttribute) || strings.Contains(string(content), "google_storage_bucket") || strings.Contains(string(content), "not a gate check") {
		t.Errorf("a detail reached the document: %s", content)
	}
	if err := validate(DocOCSFFindings, content); err != nil {
		t.Error(err)
	}
	got := map[string][]string{}
	for _, e := range doc.Events {
		got[e.FindingInfo.Title] = e.FindingInfo.Types
	}
	want := map[string][]string{"G2 validate": {"invalid_configuration"}, "G3 plan": {"plan_failed"}, "G4 standards": {"not_run"}}
	if len(got) != len(want) {
		t.Fatalf("events: %v", got)
	}
	for title, codes := range want {
		if !equalStrings(got[title], codes) {
			t.Errorf("%s: codes %v, want %v", title, got[title], codes)
		}
	}
	// OpenTofu is the tool, and a failed check of any other than secrets is
	// Medium.
	e := doc.Events[0]
	if e.SeverityID != 3 || e.Severity != "Medium" || !e.IsAlert || e.Resources[0].IaC.Tool != "opentofu" || e.Cloud.Provider != "GCP" || e.Cloud.Account != nil {
		t.Errorf("event: %+v", e)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The roots an import merged have their gate too, and the findings number
// the roots in the order of the report.
func TestFindingsOfMergedRoots(t *testing.T) {
	t.Parallel()
	r := &report.Report{}
	r.Provider.Source = "hashicorp/aws"
	r.Directories = []report.Directory{
		{Path: "aws/a", Scope: scope, Checks: []report.Check{{Name: "G1 format", Passed: true}}},
		{Path: "aws/b", Scope: scope, Checks: []report.Check{{Name: "G1 format", Passed: false}}},
	}
	r.Merges = []report.Merge{{Path: "aws/stack", Checks: []report.Check{{Name: "G7 determinism", Passed: false}, {Name: "G7 determinism", Passed: true}}}}
	doc, err := buildFindingsOfReport(r, Options{}, "6f1c9d3e-0a52-5b8e-9c47-2d1f3a5b7c90", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Events) != 2 {
		t.Fatalf("events: %+v", doc.Events)
	}
	if got := doc.Events[0].FindingInfo.UID; !strings.HasSuffix(got, ":G1:2") {
		t.Errorf("uid %q", got)
	}
	last := doc.Events[1]
	if !strings.HasSuffix(last.FindingInfo.UID, ":G7:3") || last.Resources[0].UID != "aws/stack" || last.Cloud == nil || last.Cloud.Account != nil {
		t.Errorf("merged root's finding: %+v", last)
	}
	// A check that passed in one environment and failed in another failed.
	if len(last.FindingInfo.Types) != 1 || last.FindingInfo.Types[0] != "not_deterministic" {
		t.Errorf("types: %v", last.FindingInfo.Types)
	}
}

// A verify run's roots have a path, and no iac source, which says what an
// import did.
func TestFindingsOfVerify(t *testing.T) {
	t.Parallel()
	out := writeVerifyFile(t, []map[string]any{
		verifiedRoot("clean", passedChecks()[:2]...),
		verifiedRoot("bad", failedChecks()...),
		verifiedRoot("scanners", passedChecks()...),
	})
	content := build(t, Options{Out: out, Document: DocOCSFFindings, Command: CommandVerify})
	if err := validate(DocOCSFFindings, content); err != nil {
		t.Fatal(err)
	}
	doc := decodeFindings(t, content)
	if len(doc.Events) != 2 {
		t.Fatalf("events: %+v", doc.Events)
	}
	bad, warned := doc.Events[0], doc.Events[1]
	if bad.FindingInfo.Title != "G6 secrets" || !strings.HasSuffix(bad.FindingInfo.UID, ":G6:2") || bad.Severity != "High" {
		t.Errorf("bad: %+v", bad)
	}
	if warned.FindingInfo.Title != "G5 scanners" || !strings.HasSuffix(warned.FindingInfo.UID, ":G5:3") || warned.IsAlert {
		t.Errorf("warned: %+v", warned)
	}
	for _, e := range doc.Events {
		if e.Cloud != nil || len(e.Resources) != 1 || e.Resources[0].IaC != nil || e.Resources[0].Type != "iac_root" {
			t.Errorf("%s: %+v", e.FindingInfo.Title, e)
		}
	}
	if bad.Resources[0].UID != "bad" || warned.Resources[0].UID != "scanners" {
		t.Errorf("paths: %q, %q", bad.Resources[0].UID, warned.Resources[0].UID)
	}
	// It is the verify run's run_id, as the gate's.
	gateDoc := decodeGate(t, build(t, Options{Out: out, Document: DocGate, Command: CommandVerify}))
	if gateDoc.RunID != doc.RunID {
		t.Errorf("run_id %s, the gate's %s", doc.RunID, gateDoc.RunID)
	}
	// A root that couldn't be checked has no checks, and so no finding.
	out = writeVerifyFile(t, []map[string]any{{"path": "a", "error": "init failed: " + secretAttribute}})
	content = build(t, Options{Out: out, Document: DocOCSFFindings, Command: CommandVerify})
	if doc := decodeFindings(t, content); len(doc.Events) != 0 || strings.Contains(string(content), secretAttribute) {
		t.Errorf("events: %s", content)
	}
}

func decodeGate(t *testing.T, content []byte) gateDoc {
	t.Helper()
	var doc gateDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// A gate with nothing to report has no events, and is still a document.
func TestFindingsOfACleanGate(t *testing.T) {
	t.Parallel()
	out := writeVerifyFile(t, []map[string]any{verifiedRoot("a", report.Check{Name: "G1 format", Passed: true})})
	content := build(t, Options{Out: out, Document: DocOCSFFindings, Command: CommandVerify})
	if err := validate(DocOCSFFindings, content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), `"events": []`) {
		t.Errorf("no empty list of events:\n%s", content)
	}
}

func hmacHex(key, s string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))
}

// --hash-ids hashes the path, and the state, as the OCSF inventory does;
// --drop-addresses leaves them out, and with them a resource that has
// nothing else to say.
func TestFindingsRedaction(t *testing.T) {
	t.Parallel()
	key := "0123456789abcdef-key-a"
	out := writeOut(t)
	state := func(string) string { return "s3://bucket/key.tfstate" }

	hashed := decodeFindings(t, build(t, Options{Out: out, Document: DocOCSFFindings, HashKey: []byte(key), StateURL: state}))
	res := findingOf(t, hashed, "G6").Resources[0]
	if res.UID != hmacHex(key, findingsDir) || res.IaC.Path != res.UID || res.IaC.StateKey != hmacHex(key, "s3://bucket/key.tfstate") {
		t.Errorf("hashed: %+v %+v", res, res.IaC)
	}
	if !hashed.Redaction.IDsHashed || hashed.Redaction.AddressesDropped {
		t.Errorf("redaction: %+v", hashed.Redaction)
	}
	// The same as the OCSF inventory's hashed path.
	inv := decodeOCSF(t, build(t, Options{Out: out, Document: DocOCSFInventory, HashKey: []byte(key)}))
	for _, e := range inv.Events {
		if iac := e.Resources[0].IaC; iac != nil && iac.Path != "" && iac.Path != res.IaC.Path {
			t.Errorf("the inventory's path hash %s differs from %s", iac.Path, res.IaC.Path)
		}
	}

	dropped := build(t, Options{Out: out, Document: DocOCSFFindings, DropAddresses: true, HashKey: []byte(key), StateURL: state})
	for _, leak := range []string{findingsDir, "bucket", res.UID} {
		if strings.Contains(string(dropped), leak) {
			t.Errorf("--drop-addresses left %q in:\n%s", leak, dropped)
		}
	}
	d := decodeFindings(t, dropped)
	if iac := findingOf(t, d, "G6").Resources[0].IaC; iac == nil || iac.Tool != "terraform" || iac.Path != "" || iac.StateKey != "" || iac.Outcome != importWritten {
		t.Errorf("dropped: %+v", iac)
	}
	if !d.Redaction.AddressesDropped {
		t.Errorf("redaction: %+v", d.Redaction)
	}

	// A verify run's roots have nothing but the path.
	verified := writeVerifyFile(t, []map[string]any{verifiedRoot("a", failedChecks()...)})
	v := decodeFindings(t, build(t, Options{Out: verified, Document: DocOCSFFindings, Command: CommandVerify, DropAddresses: true}))
	if len(v.Events) != 1 || v.Events[0].Resources != nil {
		t.Errorf("verify, dropped: %+v", v.Events)
	}
}

// A path that looks like a credential stops the export, unless the paths are
// left out; and a document that isn't the OCSF documents takes no repository.
func TestFindingsRefusals(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("a1", 18)
	out := writeVerifyFile(t, []map[string]any{verifiedRoot(token, failedChecks()...)})
	if _, err := Build(Options{Out: out, Document: DocOCSFFindings, Command: CommandVerify, Now: fixedNow}); err == nil {
		t.Error("a path that looks like a credential passed")
	}
	if _, err := Build(Options{Out: out, Document: DocOCSFFindings, Command: CommandVerify, DropAddresses: true, Now: fixedNow}); err != nil {
		t.Errorf("dropped: %v", err)
	}
	for name, opts := range map[string]Options{
		"request ID":       {Document: DocOCSFFindings, RequestID: "abc"},
		"bad commit":       {Document: DocOCSFFindings, CommitSHA: "abc"},
		"repo, the gate":   {Document: DocGate, RepoURL: ocsfRepo},
		"credentials":      {Document: DocOCSFFindings, RepoURL: "https://user:pw@github.com/a/b"},
		"discover command": {Document: DocOCSFFindings, Command: CommandDiscover},
		"drift command":    {Document: DocOCSFFindings, Command: CommandDrift},
	} {
		opts.Out = writeOut(t)
		if _, err := Build(opts); err == nil {
			t.Errorf("%s: worked", name)
		}
	}
}

// The titles are the names the gate gives its checks, so that a check's own
// name never reaches a document.
func TestFindingTitlesAreTheGatesChecks(t *testing.T) {
	t.Parallel()
	for _, name := range []string{gate.CheckFormat, gate.CheckValidate, gate.CheckPlan, gate.CheckStandards, gate.CheckScanners, gate.CheckSecrets, gate.CheckDeterminism, gate.CheckMoves} {
		id := checkID(name)
		if id == "" || checkTitles[id] != name {
			t.Errorf("%q: id %q, title %q", name, id, checkTitles[id])
		}
	}
	if len(checkTitles) != len(failedReason) {
		t.Errorf("%d titles, %d checks", len(checkTitles), len(failedReason))
	}
}

// The same results are the same document.
func TestFindingsAreStable(t *testing.T) {
	t.Parallel()
	a := build(t, Options{Out: writeOut(t), Document: DocOCSFFindings})
	b := build(t, Options{Out: writeOut(t), Document: DocOCSFFindings})
	if string(a) != string(b) {
		t.Errorf("differ:\n%s\n%s", a, b)
	}
}
