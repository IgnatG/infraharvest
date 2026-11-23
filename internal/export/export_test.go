// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

const scope = "aws/123456789012/eu-west-2"

// Values that must never reach an export.
const (
	secretAttribute = "Hunter2-attribute-value-0xDEADBEEF"
	secretTagValue  = "tag-value-that-is-a-password-1f9a"
	secretTagKey    = "db_password"
	keepTagKey      = "team"
	keepTagValue    = "payments"
)

// credentialTag has a GitHub token's format, put together here so that
// secret scanners of the repository don't take it for a real one.
var credentialTag = "ghp_" + strings.Repeat("a1", 18)

// writeOut writes an output directory: a report of one imported bucket,
// one resource of each owner left out, and one check of each gate result,
// and a saved inventory whose records hold attributes and tags.
func writeOut(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	r := &report.Report{}
	r.Tool = report.Component{Name: "infraharvest", Version: "v0.1.0"}
	r.Engine = report.Component{Name: "terraform", Version: "1.17.0"}
	r.Provider = report.Provider{Source: "hashicorp/aws", Constraint: "~> 6.0"}
	r.Directories = []report.Directory{{
		Path:     "aws/123456789012/eu-west-2",
		Scope:    scope,
		Imported: []report.Resource{{Address: "aws_s3_bucket.logs", ID: "logs-bucket"}},
		Checks: []report.Check{
			{Name: "G1 format", Passed: true},
			{Name: "G6 secrets", Passed: false, Details: []string{"main.tf would hold " + secretAttribute}},
			{Name: "G5 scanners", Passed: true, Skipped: true, Reason: gate.ReasonToolMissing, Details: []string{"no scanner installed (tflint, trivy, checkov)"}},
		},
	}}
	r.Exclude(scope, "aws_vpc", "vpc-state", report.Decided(report.TerraformPrefix+" (prod.tfstate)"))
	r.Exclude(scope, "aws_vpc", "vpc-tag", report.Owned(report.TerraformPrefix+" (per its tag ManagedBy=terraform)", report.OwnerTerraformTag))
	r.Exclude(scope, "aws_vpc", "vpc-cfn", report.Decided("managed by CloudFormation stack app"))
	r.Exclude(scope, "aws_vpc", "vpc-default", report.Decided("default VPC, which AWS creates in every region"))
	r.AddDiscovered(scope, 5)
	r.Finish(map[string]int{"aws_s3_bucket": 1, "aws_vpc": 4}, nil, false)
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	inventory := map[string]any{
		"version": 2, "provider": "aws", "scope": scope, "services": []string{"s3", "vpc"},
		"records": []map[string]any{
			{"service": "s3", "provider": "aws", "type": "aws_s3_bucket", "id": "logs-bucket", "name": "logs",
				"tags":       map[string]string{keepTagKey: keepTagValue, secretTagKey: secretTagValue, "owner-note": credentialTag, "env": "prod"},
				"attributes": map[string]string{"master_password": secretAttribute}},
			{"service": "vpc", "provider": "aws", "type": "aws_vpc", "id": "vpc-state", "name": "main",
				"attributes": map[string]string{"secret_key": secretAttribute}},
		},
	}
	content, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(out, inventoryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aws-0123456789abcdef.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return out
}

var fixedNow = func() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC) }

func build(t *testing.T, opts Options) []byte {
	t.Helper()
	opts.Now = fixedNow
	content, err := Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func decodeInventory(t *testing.T, content []byte) inventoryDoc {
	t.Helper()
	var doc inventoryDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestNoSecretReachesAnyDocument(t *testing.T) {
	t.Parallel()
	out := writeOutWithDrift(t)
	for _, document := range Documents {
		content := string(build(t, Options{Out: out, Document: document}))
		for _, leak := range []string{secretAttribute, secretTagValue, credentialTag, driftSecretBefore, driftSecretAfter, "attributes", "master_password", "aws_s3_bucket.logs", "tfstate"} {
			// The OCSF inventory and the graph have the addresses of what the
			// import took over, unless they are left out.
			if leak == "aws_s3_bucket.logs" && (document == DocOCSFInventory || document == DocGraph) {
				continue
			}
			if strings.Contains(content, leak) {
				t.Errorf("%s holds %q:\n%s", document, leak, content)
			}
		}
		if document == DocOCSFInventory || document == DocGraph {
			if content := string(build(t, Options{Out: out, Document: document, DropAddresses: true})); strings.Contains(content, "aws_s3_bucket.logs") {
				t.Errorf("%s holds an address although they are left out:\n%s", document, content)
			}
		}
	}
}

func TestInventory(t *testing.T) {
	t.Parallel()
	doc := decodeInventory(t, build(t, Options{Out: writeOut(t), Document: DocInventory}))
	if doc.Schema != schemaInventory || doc.Cloud != "aws" || doc.GeneratedAt != "2026-11-02T10:00:00Z" {
		t.Errorf("header: %+v", doc)
	}
	byID := map[string]resource{}
	for _, res := range doc.Resources {
		byID[res.ID] = res
	}
	logs := byID["logs-bucket"]
	if logs.Name != "logs" || logs.Account != "123456789012" || logs.Region != "eu-west-2" || logs.Status != report.StatusUnmanaged {
		t.Errorf("imported bucket: %+v", logs)
	}
	if len(logs.Tags) != 2 || logs.Tags[keepTagKey] != keepTagValue || logs.Tags["env"] != "prod" {
		t.Errorf("tags kept: %v", logs.Tags)
	}
	// The key of a tag whose value is a credential is listed; the secret
	// itself is not.
	if got := strings.Join(doc.Redaction.TagKeysDropped, ","); got != secretTagKey+",owner-note" || doc.Redaction.IDsHashed {
		t.Errorf("redaction: %+v", doc.Redaction)
	}
	for id, want := range map[string][3]string{
		"vpc-state":   {report.StatusManaged, report.BasisState, report.OwnerTerraformState},
		"vpc-tag":     {report.StatusManaged, report.BasisTag, report.OwnerTerraformTag},
		"vpc-cfn":     {report.StatusOtherIaC, report.BasisTag, report.OwnerCloudFormation},
		"vpc-default": {report.StatusUnmanaged, report.BasisNone, report.OwnerCloud},
	} {
		res := byID[id]
		if res.Status != want[0] || res.StatusBasis != want[1] || res.Owner != want[2] {
			t.Errorf("%s: got %s/%s/%s, want %v", id, res.Status, res.StatusBasis, res.Owner, want)
		}
	}
	if byID["vpc-state"].Name != "main" {
		t.Errorf("the inventory's name is missing: %+v", byID["vpc-state"])
	}
	if doc.Coverage.Totals.Discovered != 5 || doc.Coverage.Totals.Managed != 2 || doc.Coverage.Totals.Imported != 1 || len(doc.Coverage.Scopes) != 1 {
		t.Errorf("coverage: %+v", doc.Coverage)
	}
	if len(doc.Resources) != 5 {
		t.Errorf("%d resources, want 5", len(doc.Resources))
	}
}

func TestWithoutSavedInventory(t *testing.T) {
	t.Parallel()
	out := writeOut(t)
	if err := os.RemoveAll(filepath.Join(out, inventoryDir)); err != nil {
		t.Fatal(err)
	}
	doc := decodeInventory(t, build(t, Options{Out: out, Document: DocInventory}))
	if len(doc.Resources) != 5 {
		t.Fatalf("%d resources, want the report's 5", len(doc.Resources))
	}
	for _, res := range doc.Resources {
		if res.Name != "" || res.Tags != nil {
			t.Errorf("%+v has what only the inventory knows", res)
		}
	}
}

func TestDropTags(t *testing.T) {
	t.Parallel()
	out := writeOut(t)
	one := decodeInventory(t, build(t, Options{Out: out, Document: DocInventory, DropTags: []string{keepTagKey}}))
	for _, res := range one.Resources {
		if _, ok := res.Tags[keepTagKey]; ok {
			t.Errorf("%s kept %s", res.ID, keepTagKey)
		}
	}
	if got := strings.Join(one.Redaction.TagKeysDropped, ","); got != secretTagKey+",owner-note,"+keepTagKey {
		t.Errorf("dropped keys: %s", got)
	}
	all := decodeInventory(t, build(t, Options{Out: out, Document: DocInventory, DropAllTags: true}))
	for _, res := range all.Resources {
		if res.Tags != nil {
			t.Errorf("%s kept tags %v", res.ID, res.Tags)
		}
	}
	if !all.Redaction.AllTagsDropped {
		t.Error("the export doesn't say it dropped all tags")
	}
}

// A tag whose key is itself a credential is dropped and never listed among
// the dropped keys, whichever way it came to be dropped.
func TestCredentialKeyNeverListed(t *testing.T) {
	t.Parallel()
	tags := map[string]string{credentialTag: "x", "team": "data"}
	for name, r := range map[string]*redactor{
		"detected":  newRedactor(nil, false, nil),
		"drop-tag":  newRedactor([]string{credentialTag}, false, nil),
		"drop-tags": newRedactor(nil, true, nil),
	} {
		kept := r.tags(tags, "", "")
		if _, ok := kept[credentialTag]; ok {
			t.Errorf("%s: kept the credential key", name)
		}
		for _, k := range r.droppedKeys() {
			if k == credentialTag {
				t.Errorf("%s: listed the credential key as dropped", name)
			}
		}
	}
}

func TestHashing(t *testing.T) {
	t.Parallel()
	out := writeOut(t)
	keyA, keyB := []byte("0123456789abcdef-key-a"), []byte("0123456789abcdef-key-b")
	hashed := func(key []byte) inventoryDoc {
		return decodeInventory(t, build(t, Options{Out: out, Document: DocInventory, HashKey: key}))
	}
	a1, a2, b := hashed(keyA), hashed(keyA), hashed(keyB)
	if !a1.Redaction.IDsHashed {
		t.Error("the export doesn't say it hashed IDs")
	}
	for i, res := range a1.Resources {
		if res.ID != a2.Resources[i].ID || res.Name != a2.Resources[i].Name {
			t.Errorf("%s: the same key gave other hashes", res.Type)
		}
		if res.ID == b.Resources[i].ID {
			t.Errorf("%s: another key gave the same hash", res.Type)
		}
		if len(res.ID) != 64 || strings.Contains(res.ID, "vpc") || strings.Contains(res.ID, "logs-bucket") {
			t.Errorf("ID not hashed: %s", res.ID)
		}
		if res.Name != "" && (res.Name == "logs" || res.Name == "main") {
			t.Errorf("name not hashed: %s", res.Name)
		}
		for k, v := range res.Tags {
			if strings.Contains(k, "logs-bucket") || strings.Contains(v, "logs-bucket") || v == "logs" || v == "main" {
				t.Errorf("%s: the tag %s=%s gives away a hashed ID or name", res.Type, k, v)
			}
		}
	}
}

// With --hash-ids, a tag doesn't give away the ID or name hashed: the Name
// tag and values that are or hold them are hashed alike, and a key that
// holds one is dropped without being listed. Other tags stay.
func TestHashingTags(t *testing.T) {
	t.Parallel()
	r := newRedactor(nil, false, []byte("0123456789abcdef-key"))
	got := r.tags(map[string]string{
		"Name":                              "web",
		"arn":                               "arn:aws:s3:::logs-bucket",
		"alias":                             "logs",
		"kubernetes.io/cluster/logs-bucket": "owned",
		"env":                               "prod",
		"tier":                              "a",
	}, "logs-bucket", "logs")
	want := map[string]string{
		"Name":  r.hash("web"),
		"arn":   r.hash("arn:aws:s3:::logs-bucket"),
		"alias": r.hash("logs"),
		"env":   "prod",
		"tier":  "a",
	}
	if !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if keys := r.droppedKeys(); len(keys) != 0 {
		t.Errorf("listed as dropped: %v", keys)
	}
	// Nor is such a key listed when it is dropped another way: by
	// --drop-tags, --drop-tag, or as looking secret.
	for _, dropped := range []*redactor{
		newRedactor(nil, true, []byte("0123456789abcdef-key")),
		newRedactor([]string{"kubernetes.io/cluster/logs-bucket"}, false, []byte("0123456789abcdef-key")),
	} {
		dropped.tags(map[string]string{"kubernetes.io/cluster/logs-bucket": "owned", "env": "prod"}, "logs-bucket", "logs")
		if keys := dropped.droppedKeys(); slices.Contains(keys, "kubernetes.io/cluster/logs-bucket") {
			t.Errorf("a key naming the resource was listed as dropped: %v", keys)
		}
	}
	// Without --hash-ids, tags stay as they are.
	plain := newRedactor(nil, false, nil).tags(map[string]string{"Name": "web", "alias": "logs"}, "logs-bucket", "logs")
	if plain["Name"] != "web" || plain["alias"] != "logs" {
		t.Errorf("tags changed without --hash-ids: %v", plain)
	}
}

func TestReadKey(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("  a-key-with-enough-bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	none := func(string) string { return "" }
	fromEnv := func(k string) string {
		if k == KeyEnv {
			return "an-environment-key-0123"
		}
		return ""
	}
	if _, err := ReadKey("", none); err == nil {
		t.Error("no key was accepted")
	}
	if key, err := ReadKey("", fromEnv); err != nil || string(key) != "an-environment-key-0123" {
		t.Errorf("environment: %q, %v", key, err)
	}
	if key, err := ReadKey(file, fromEnv); err != nil || string(key) != "a-key-with-enough-bytes" {
		t.Errorf("file wins and is trimmed: %q, %v", key, err)
	}
	if _, err := ReadKey(filepath.Join(t.TempDir(), "missing"), none); err == nil {
		t.Error("a missing key file was accepted")
	}
	if _, err := ReadKey("", func(string) string { return "short" }); err == nil {
		t.Error("a short key was accepted")
	}
}

func TestSchemaRejectsExtraFields(t *testing.T) {
	t.Parallel()
	out := writeOutWithDrift(t)
	for _, document := range Documents {
		good := build(t, Options{Out: out, Document: document})
		if err := validate(document, good); err != nil {
			t.Fatalf("%s: %v", document, err)
		}
		var top map[string]any
		if err := json.Unmarshal(good, &top); err != nil {
			t.Fatal(err)
		}
		top["attributes"] = map[string]string{"password": "x"}
		bad, _ := json.Marshal(top)
		if err := validate(document, bad); err == nil {
			t.Errorf("%s: an extra field at the top passed", document)
		}
	}
	var inv map[string]any
	if err := json.Unmarshal(build(t, Options{Out: out, Document: DocInventory}), &inv); err != nil {
		t.Fatal(err)
	}
	inv["resources"].([]any)[0].(map[string]any)["attributes"] = map[string]string{"password": "x"}
	bad, _ := json.Marshal(inv)
	if err := validate(DocInventory, bad); err == nil {
		t.Error("an extra field in a resource passed")
	}
	inv["resources"].([]any)[0].(map[string]any)["status"] = "mine"
	bad, _ = json.Marshal(inv)
	if err := validate(DocInventory, bad); err == nil {
		t.Error("a status outside the list passed")
	}
}

func TestRun(t *testing.T) {
	t.Parallel()
	content := build(t, Options{Out: writeOut(t), Document: DocRun})
	var doc runDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	// The G6 check failed, so the import is incomplete.
	if doc.ExitCode != report.ExitIncomplete || doc.Outcome != outcomeFailed || doc.Command != "import" {
		t.Errorf("run: %+v", doc)
	}
	if got := strings.Join(doc.FailureCodes, ","); got != failCheck {
		t.Errorf("failure codes: %s", got)
	}
	if doc.Engine.Name != "terraform" || doc.Tool.Version != "v0.1.0" || doc.Coverage.Discovered != 5 {
		t.Errorf("run: %+v", doc)
	}
}

func TestOutcomes(t *testing.T) {
	t.Parallel()
	for code, want := range map[int]string{
		report.ExitOK: outcomeOK, report.ExitPartial: outcomePartial, report.ExitIncomplete: outcomeFailed,
		report.ExitCouldNotRun: outcomeFailed, report.ExitInterrupted: outcomeInterrupted, report.ExitTerminated: outcomeInterrupted,
	} {
		if got := outcomeOf(code); got != want {
			t.Errorf("exit %d: got %s, want %s", code, got, want)
		}
	}
}

func TestGate(t *testing.T) {
	t.Parallel()
	content := build(t, Options{Out: writeOut(t), Document: DocGate})
	var doc gateDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Passed || len(doc.Checks) != 3 {
		t.Fatalf("gate: %+v", doc)
	}
	want := map[string]gateCheck{
		"G1": {ID: "G1", Passed: true, ReasonCodes: []string{}},
		"G5": {ID: "G5", Passed: true, Skipped: true, ReasonCodes: []string{reasonToolMissing}},
		"G6": {ID: "G6", Passed: false, ReasonCodes: []string{reasonSecrets}},
	}
	for _, c := range doc.Checks {
		w := want[c.ID]
		if c.Passed != w.Passed || c.Skipped != w.Skipped || strings.Join(c.ReasonCodes, ",") != strings.Join(w.ReasonCodes, ",") {
			t.Errorf("%s: got %+v, want %+v", c.ID, c, w)
		}
	}
}

func TestReasonCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		id    string
		check report.Check
		want  string
	}{
		{"failed", "G2", report.Check{Passed: false, Details: []string{"main.tf:3 anything"}}, reasonInvalid},
		{"never ran", "G3", report.Check{Passed: false, Reason: gate.ReasonNotRun, Details: []string{"didn't run"}}, reasonNotRun},
		{"no scanner", "G5", report.Check{Passed: true, Skipped: true, Reason: gate.ReasonToolMissing, Details: []string{"no scanner installed (tflint)"}}, reasonToolMissing},
		{"held back", "G5", report.Check{Passed: true, Skipped: true, Reason: gate.ReasonNotRun, Details: []string{"not run, as G6 secrets found secret values"}}, reasonNotRun},
		{"not planned", "G3", report.Check{Passed: true, Skipped: true, Reason: gate.ReasonNotRun, Details: []string{"environment dev", "not planned: the import didn't have it"}}, reasonNotRun},
		{"unknown skip", "G3", report.Check{Passed: true, Skipped: true, Details: []string{"some new reason with i-0abc"}}, reasonOther},
		// The details are free text: only the reason decides the code.
		{"never ran, reworded", "G3", report.Check{Passed: false, Reason: gate.ReasonNotRun, Details: []string{"this step was left out"}}, reasonNotRun},
		{"no scanner, reworded", "G5", report.Check{Passed: true, Skipped: true, Reason: gate.ReasonToolMissing, Details: []string{"nothing to scan with"}}, reasonToolMissing},
		{"held back, reworded", "G5", report.Check{Passed: true, Skipped: true, Reason: gate.ReasonNotRun, Details: []string{"waiting"}}, reasonNotRun},
		{"failed, text of a skip", "G3", report.Check{Passed: false, Details: []string{"didn't run"}}, reasonPlanFailed},
		{"skipped, text of a missing tool", "G5", report.Check{Passed: true, Skipped: true, Details: []string{"no scanner installed"}}, reasonOther},
		{"skipped, text of not run", "G3", report.Check{Passed: true, Skipped: true, Details: []string{"not run", "not planned"}}, reasonOther},
		{"formatting", "G1", report.Check{Passed: false}, reasonNotFormatted},
		{"validate", "G2", report.Check{Passed: false}, reasonInvalid},
		{"standards", "G4", report.Check{Passed: false}, reasonStandards},
		{"scanners", "G5", report.Check{Passed: false}, reasonScanners},
		{"secrets", "G6", report.Check{Passed: false}, reasonSecrets},
		{"determinism", "G7", report.Check{Passed: false}, reasonNotDeterm},
		{"moves", "G8", report.Check{Passed: false}, reasonMovesInvalid},
	} {
		got := reasonCodes(tc.id, tc.check)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: got %v, want %s", tc.name, got, tc.want)
		}
	}
	if got := reasonCodes("G1", report.Check{Passed: true}); got != nil {
		t.Errorf("a passing check has reasons: %v", got)
	}
}

func TestRunIDIsStable(t *testing.T) {
	t.Parallel()
	out := writeOutWithDrift(t)
	var ids []string
	for _, document := range Documents {
		var doc struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(build(t, Options{Out: out, Document: document}), &doc); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, doc.RunID)
	}
	if ids[0] != ids[1] || ids[1] != ids[2] || len(ids[0]) != 36 {
		t.Errorf("run IDs: %v", ids)
	}
}

func TestUnknownDocumentAndMissingReport(t *testing.T) {
	t.Parallel()
	if _, err := Build(Options{Out: writeOut(t), Document: "everything"}); err == nil {
		t.Error("an unknown document was built")
	}
	if _, err := Build(Options{Out: t.TempDir(), Document: DocRun}); err == nil {
		t.Error("an export without a report was built")
	}
}

func TestSchemasAreEmbeddedAndNamed(t *testing.T) {
	t.Parallel()
	for _, document := range Documents {
		content, err := Schema(document)
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			ID                   string `json:"$id"`
			Dialect              string `json:"$schema"`
			AdditionalProperties *bool  `json:"additionalProperties"`
		}
		if err := json.Unmarshal(content, &s); err != nil {
			t.Fatal(err)
		}
		if want := "https://ignatg.github.io/infraharvest/schemas/" + document + "-v1.schema.json"; s.ID != want {
			t.Errorf("%s: $id %s, want %s", document, s.ID, want)
		}
		if s.Dialect != "https://json-schema.org/draft/2020-12/schema" || s.AdditionalProperties == nil || *s.AdditionalProperties {
			t.Errorf("%s: dialect or additionalProperties: %+v", document, s)
		}
		if strings.Count(string(content), `"additionalProperties": {"type"`) > 1 {
			// Only the tags map may take any key.
			t.Errorf("%s: free-form objects beyond tags", document)
		}
	}
	if _, err := Schema("nothing"); err == nil {
		t.Error("an unknown schema was found")
	}
}

// The saved inventories this reads are in the format the importer writes.
func TestInventoryVersionIsTheImporters(t *testing.T) {
	t.Parallel()
	if inventoryVersion != importer.InventoryVersion {
		t.Errorf("inventoryVersion %d, want the importer's %d", inventoryVersion, importer.InventoryVersion)
	}
}

func TestInventoryListFileIsTheImporters(t *testing.T) {
	t.Parallel()
	if inventoryListFile != importer.InventoryListFile {
		t.Errorf("inventoryListFile %s, want the importer's %s", inventoryListFile, importer.InventoryListFile)
	}
}

func TestInventoryDirIsTheImporters(t *testing.T) {
	t.Parallel()
	if want := filepath.Join(importer.CheckpointDir, "inventory"); inventoryDir != want {
		t.Errorf("inventoryDir %s, want %s", inventoryDir, want)
	}
}

func TestRunTimes(t *testing.T) {
	t.Parallel()
	out := writeOut(t)
	started := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	finished := started.Add(7 * time.Minute)

	var run runDoc
	if err := json.Unmarshal(build(t, Options{Out: out, Document: DocRun, StartedAt: started, FinishedAt: finished}), &run); err != nil {
		t.Fatal(err)
	}
	if run.StartedAt != "2026-11-03T09:30:00Z" || run.FinishedAt != "2026-11-03T09:37:00Z" {
		t.Errorf("run times: %q, %q", run.StartedAt, run.FinishedAt)
	}

	// Without times, the run document has none.
	run = runDoc{}
	if err := json.Unmarshal(build(t, Options{Out: out, Document: DocRun}), &run); err != nil {
		t.Fatal(err)
	}
	if run.StartedAt != "" || run.FinishedAt != "" {
		t.Errorf("run times without any: %q, %q", run.StartedAt, run.FinishedAt)
	}

	// An offset is the same instant, in UTC.
	run = runDoc{}
	other := time.FixedZone("x", 2*3600)
	if err := json.Unmarshal(build(t, Options{Out: out, Document: DocRun, StartedAt: started.In(other)}), &run); err != nil {
		t.Fatal(err)
	}
	if run.StartedAt != "2026-11-03T09:30:00Z" || run.FinishedAt != "" {
		t.Errorf("run times: %q, %q", run.StartedAt, run.FinishedAt)
	}

	if _, err := Build(Options{Out: out, Document: DocRun, StartedAt: finished, FinishedAt: started}); err == nil {
		t.Error("a run that finished before it started worked")
	}
}

// Exporting the same run twice says the same time, later or not.
func TestGeneratedAtIsFinishedAt(t *testing.T) {
	t.Parallel()
	out := writeOut(t)
	finished := time.Date(2026, 11, 3, 9, 37, 0, 0, time.UTC)
	var first, second inventoryDoc
	for i, doc := range []*inventoryDoc{&first, &second} {
		opts := Options{Out: out, Document: DocInventory, FinishedAt: finished}
		opts.Now = func() time.Time { return time.Date(2026, 11, 3, 12+i, 0, 0, 0, time.UTC) }
		content, err := Build(opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(content, doc); err != nil {
			t.Fatal(err)
		}
	}
	if first.GeneratedAt != "2026-11-03T09:37:00Z" || second.GeneratedAt != first.GeneratedAt || first.RunID != second.RunID {
		t.Errorf("generated_at: %q then %q", first.GeneratedAt, second.GeneratedAt)
	}
}

func TestParseTime(t *testing.T) {
	t.Parallel()
	got, err := ParseTime("--started-at", "2026-11-03T11:30:00+02:00")
	if err != nil || formatTime(got) != "2026-11-03T09:30:00Z" {
		t.Errorf("offset: %v, %v", got, err)
	}
	for _, bad := range []string{"yesterday", "2026-11-03", "2026-11-03 09:30:00", "1730626200", "2026-13-03T09:30:00Z"} {
		if _, err := ParseTime("--started-at", bad); err == nil || !strings.Contains(err.Error(), "--started-at") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
