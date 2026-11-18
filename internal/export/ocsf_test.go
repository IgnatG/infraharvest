// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

const (
	ocsfDir         = "aws/123456789012/eu-west-2"
	ocsfCommit      = "0123456789abcdef0123456789abcdef01234567"
	ocsfRepo        = "https://github.com/example/infrastructure"
	ocsfModuleAddr  = `module.net["a.b"].module.sub`
	ocsfModuleRes   = ocsfModuleAddr + `.aws_vpc.main`
	ocsfBucketAddr  = "aws_s3_bucket.logs"
	ocsfStateVPC    = "vpc-state"
	ocsfModuleVPCID = "vpc-in-module"
)

// writeOCSFOut writes an output directory of an import of one bucket, at
// the root of the output's directory, and of a VPC in a nested module call,
// with one VPC that a state manages and the saved inventory of writeOut.
// dir is the root's path in the output; imported adds to what it took over.
func writeOCSFOut(t *testing.T, dir string, imported ...report.Resource) string {
	t.Helper()
	out := t.TempDir()
	r := &report.Report{}
	r.Tool = report.Component{Name: "infraharvest", Version: "v0.1.0"}
	r.Engine = report.Component{Name: "terraform", Version: "1.17.0"}
	r.Provider = report.Provider{Source: "hashicorp/aws", Constraint: "~> 6.0"}
	r.Directories = []report.Directory{{
		Path:  dir,
		Scope: scope,
		Imported: append([]report.Resource{
			{Address: ocsfBucketAddr, ID: "logs-bucket"},
			{Address: ocsfModuleRes, ID: ocsfModuleVPCID},
		}, imported...),
	}}
	r.Exclude(scope, "aws_vpc", ocsfStateVPC, report.Decided(report.TerraformPrefix+" (prod.tfstate)"))
	r.Exclude(scope, "aws_vpc", "vpc-default", report.Decided("default VPC, which AWS creates in every region"))
	r.AddDiscovered(scope, 4)
	r.Finish(map[string]int{"aws_s3_bucket": 1, "aws_vpc": 3}, nil, false)
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	inventory := map[string]any{
		"version": 2, "provider": "aws", "scope": scope, "services": []string{"s3", "vpc"},
		"records": []map[string]any{
			{"service": "s3", "provider": "aws", "type": "aws_s3_bucket", "id": "logs-bucket", "name": "logs",
				"tags":       map[string]string{keepTagKey: keepTagValue, secretTagKey: secretTagValue, "owner-note": credentialTag, "env": "prod"},
				"attributes": map[string]string{"master_password": secretAttribute}},
			{"service": "vpc", "provider": "aws", "type": "aws_vpc", "id": ocsfStateVPC, "name": "main"},
		},
	}
	content, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	invDir := filepath.Join(out, inventoryDir)
	if err := os.MkdirAll(invDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invDir, "aws-0123456789abcdef.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeOCSF(t *testing.T, content []byte) ocsfDoc {
	t.Helper()
	var doc ocsfDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// eventOf returns the event of the resource with the ID.
func eventOf(t *testing.T, doc ocsfDoc, id string) ocsfEvent {
	t.Helper()
	for _, e := range doc.Events {
		if len(e.Resources) == 1 && e.Resources[0].UID == id {
			return e
		}
	}
	t.Fatalf("no event for %s", id)
	return ocsfEvent{}
}

func TestOCSFInventory(t *testing.T) {
	t.Parallel()
	out := writeOCSFOut(t, ocsfDir)
	doc := decodeOCSF(t, build(t, Options{Out: out, Document: DocOCSFInventory, RepoURL: ocsfRepo + ".git/", CommitSHA: ocsfCommit}))
	if doc.Schema != schemaOCSFInventory || doc.OCSFVersion != "1.9.0" || len(doc.RunID) != 36 {
		t.Errorf("header: %+v", doc)
	}

	// The same resources as the inventory has, in its order, one event each.
	inv := decodeInventory(t, build(t, Options{Out: out, Document: DocInventory}))
	if len(doc.Events) != len(inv.Resources) || len(doc.Events) != 4 {
		t.Fatalf("%d events for %d resources", len(doc.Events), len(inv.Resources))
	}
	for i, res := range inv.Resources {
		if got := doc.Events[i].Resources; len(got) != 1 || got[0].Type != res.Type || got[0].UID != res.ID {
			t.Errorf("event %d is of %+v, want %s %s", i, got, res.Type, res.ID)
		}
	}
	if doc.RunID != inv.RunID || doc.Events[0].Metadata.CorrelationUID != inv.RunID {
		t.Errorf("run IDs: %s, %s, %s", doc.RunID, doc.Events[0].Metadata.CorrelationUID, inv.RunID)
	}

	// What OCSF requires of a Cloud Resources Inventory Info event, in the
	// category Discovery (OCSF 1.9.0).
	for _, e := range doc.Events {
		if e.ClassUID != 5023 || e.CategoryUID != 5 || e.ActivityID != 2 || e.SeverityID != 1 || e.TypeUID != e.ClassUID*100+e.ActivityID {
			t.Errorf("classification: %+v", e)
		}
		if e.Time != fixedNow().UnixMilli() {
			t.Errorf("time %d, want %d", e.Time, fixedNow().UnixMilli())
		}
		m := e.Metadata
		if m.Version != "1.9.0" || m.Product.Name != "infraharvest" || m.Product.Version != "v0.1.0" ||
			!slices.Equal(m.Profiles, []string{"cloud", "iac/iac_source"}) || len(m.Extensions) != 1 || m.Extensions[0].Name != "iac" {
			t.Errorf("metadata: %+v", m)
		}
		if e.Cloud.Provider != "AWS" || e.Cloud.Account == nil || e.Cloud.Account.UID != "123456789012" || e.Cloud.Region != "eu-west-2" {
			t.Errorf("cloud: %+v", e.Cloud)
		}
	}

	logs := eventOf(t, doc, "logs-bucket").Resources[0]
	if logs.Type != "aws_s3_bucket" || logs.Name != "logs" || logs.Region != "eu-west-2" {
		t.Errorf("bucket: %+v", logs)
	}
	// Tags that look like secrets are dropped, as in the inventory; the
	// rest are key-value objects, sorted.
	if want := []ocsfTag{{"env", "prod"}, {keepTagKey, keepTagValue}}; !slices.Equal(logs.Tags, want) {
		t.Errorf("tags: %v, want %v", logs.Tags, want)
	}
	if want := []string{"status:unmanaged", "status_basis:none"}; !slices.Equal(logs.Labels, want) {
		t.Errorf("labels: %v, want %v", logs.Labels, want)
	}
	if want := (&iacSource{Tool: "terraform", Address: ocsfBucketAddr, Path: ocsfDir, RepoURL: ocsfRepo, CommitSHA: ocsfCommit}); logs.IaC == nil || *logs.IaC != *want {
		t.Errorf("iac of the bucket: %+v, want %+v", logs.IaC, want)
	}
	// A resource in a module call says which, nested and keyed.
	if got := eventOf(t, doc, ocsfModuleVPCID).Resources[0].IaC; got == nil || got.Address != ocsfModuleRes || got.ModuleAddress != ocsfModuleAddr {
		t.Errorf("iac of the module's VPC: %+v", got)
	}
	// What the import didn't take over has no address in the
	// configuration, so no iac source, but still says who manages it.
	state := eventOf(t, doc, ocsfStateVPC).Resources[0]
	if state.IaC != nil || state.Name != "main" || !slices.Contains(state.Labels, "owner:terraform_state") || !slices.Contains(state.Labels, "status:managed") {
		t.Errorf("a VPC a state manages: %+v", state)
	}
}

// Without a repository, a commit, or an engine the tool is known by, the
// events say less, and OpenTofu is named as such.
func TestOCSFInventoryOmitsWhatItDoesNotKnow(t *testing.T) {
	t.Parallel()
	out := writeOCSFOut(t, ocsfDir)
	doc := decodeOCSF(t, build(t, Options{Out: out, Document: DocOCSFInventory}))
	got := eventOf(t, doc, "logs-bucket").Resources[0].IaC
	if got == nil || got.RepoURL != "" || got.CommitSHA != "" || got.Workspace != "" || got.StateKey != "" || got.Tool != "terraform" {
		t.Errorf("iac: %+v", got)
	}
	for _, name := range []string{"repo_url", "commit_sha", "workspace", "state_key"} {
		if strings.Contains(string(build(t, Options{Out: out, Document: DocOCSFInventory})), `"`+name+`"`) {
			t.Errorf("the document names %s", name)
		}
	}

	r, err := report.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	for engine, want := range map[string]string{"tofu": "opentofu", "OpenTofu": "opentofu", "terraform": "terraform", "other": ""} {
		if got := toolOf(report.Component{Name: engine}); got != want {
			t.Errorf("engine %q: tool %q, want %q", engine, got, want)
		}
	}
	r.Engine = report.Component{Name: "tofu", Version: "1.10.0"}
	doc2, err := buildOCSFInventory(r, Options{Out: out}, "id", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	if got := eventOf(t, *doc2, "logs-bucket").Resources[0].IaC; got == nil || got.Tool != "opentofu" {
		t.Errorf("tofu: %+v", got)
	}
	r.Engine = report.Component{Name: "something"}
	doc2, err = buildOCSFInventory(r, Options{Out: out}, "id", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	if got := eventOf(t, *doc2, "logs-bucket").Resources[0].IaC; got == nil || got.Tool != "" || got.Address != ocsfBucketAddr {
		t.Errorf("unknown engine: %+v", got)
	}
}

func TestOCSFInventoryDropTags(t *testing.T) {
	t.Parallel()
	out := writeOCSFOut(t, ocsfDir)
	one := decodeOCSF(t, build(t, Options{Out: out, Document: DocOCSFInventory, DropTags: []string{keepTagKey}}))
	if want := []ocsfTag{{"env", "prod"}}; !slices.Equal(eventOf(t, one, "logs-bucket").Resources[0].Tags, want) {
		t.Errorf("--drop-tag: %v", eventOf(t, one, "logs-bucket").Resources[0].Tags)
	}
	all := build(t, Options{Out: out, Document: DocOCSFInventory, DropAllTags: true})
	if strings.Contains(string(all), `"tags"`) {
		t.Errorf("--drop-tags left tags:\n%s", all)
	}
	for _, leak := range []string{secretTagValue, credentialTag, secretAttribute} {
		if strings.Contains(string(build(t, Options{Out: out, Document: DocOCSFInventory})), leak) {
			t.Errorf("the OCSF inventory holds %q", leak)
		}
	}
	// The key of a dropped tag is only in the redaction, as in the inventory.
	if doc := decodeOCSF(t, build(t, Options{Out: out, Document: DocOCSFInventory})); strings.Count(string(build(t, Options{Out: out, Document: DocOCSFInventory})), secretTagKey) != 1 || !slices.Contains(doc.Redaction.TagKeysDropped, secretTagKey) {
		t.Errorf("the key of the dropped tag: %v", doc.Redaction.TagKeysDropped)
	}
}

func TestOCSFInventoryHashIDs(t *testing.T) {
	t.Parallel()
	out := writeOCSFOut(t, ocsfDir)
	keyA, keyB := []byte("0123456789abcdef-key-a"), []byte("0123456789abcdef-key-b")
	opts := func(key []byte) Options {
		return Options{Out: out, Document: DocOCSFInventory, HashKey: key, RepoURL: ocsfRepo, CommitSHA: ocsfCommit}
	}
	a1, a2, b := decodeOCSF(t, build(t, opts(keyA))), decodeOCSF(t, build(t, opts(keyA))), decodeOCSF(t, build(t, opts(keyB)))
	plain := decodeOCSF(t, build(t, opts(nil)))
	red := newRedactor(nil, false, keyA)
	for i, e := range a1.Events {
		res := e.Resources[0]
		if len(res.UID) != 64 || res.UID != a2.Events[i].Resources[0].UID || res.UID == b.Events[i].Resources[0].UID {
			t.Errorf("ID %s: not hashed, or not by the key", res.UID)
		}
		if res.Name != "" && len(res.Name) != 64 {
			t.Errorf("name not hashed: %s", res.Name)
		}
		// The account and the region, as the type, stay as they are.
		if e.Cloud.Account.UID != "123456789012" || e.Cloud.Region != "eu-west-2" || res.Type != plain.Events[i].Resources[0].Type {
			t.Errorf("hashed: %+v", e)
		}
		src := res.IaC
		if src == nil {
			continue
		}
		pl := plain.Events[i].Resources[0].IaC
		// The address, module and path are hashed as the drift
		// summary hashes an address, so that they match across exports.
		if src.Address != red.hash(pl.Address) || src.Path != red.hash(pl.Path) || src.ModuleAddress != red.hash(pl.ModuleAddress) {
			t.Errorf("hashes: %+v from %+v", src, pl)
		}
		if src.Address == b.Events[i].Resources[0].IaC.Address {
			t.Errorf("another key gave the same address hash")
		}
		// The tool, the repository and the commit are no names of a resource.
		if src.Tool != "terraform" || src.RepoURL != ocsfRepo || src.CommitSHA != ocsfCommit {
			t.Errorf("iac: %+v", src)
		}
	}
	if eventOf(t, a1, red.hash(ocsfModuleVPCID)).Resources[0].IaC.ModuleAddress == "" {
		t.Error("the module address is gone")
	}
	content := string(build(t, opts(keyA)))
	for _, leak := range []string{"logs-bucket", `"logs"`, ocsfBucketAddr, "module.net", "aws_vpc.main", ocsfDir, ocsfModuleVPCID, ocsfStateVPC, `"main"`} {
		if strings.Contains(content, leak) {
			t.Errorf("the hashed document holds %q:\n%s", leak, content)
		}
	}
}

func TestOCSFInventoryDropAddresses(t *testing.T) {
	t.Parallel()
	out := writeOCSFOut(t, ocsfDir)
	for _, key := range [][]byte{nil, []byte("0123456789abcdef-key-a")} {
		content := build(t, Options{Out: out, Document: DocOCSFInventory, HashKey: key, DropAddresses: true, RepoURL: ocsfRepo, CommitSHA: ocsfCommit})
		for _, leak := range []string{`"address"`, `"module_address"`, `"path"`, ocsfBucketAddr, "module.net", "aws_vpc.main", ocsfDir} {
			if strings.Contains(string(content), leak) {
				t.Errorf("the document without addresses holds %q:\n%s", leak, content)
			}
		}
		doc := decodeOCSF(t, content)
		if len(doc.Events) != 4 {
			t.Fatalf("%d events", len(doc.Events))
		}
		// What isn't an address stays, as the counts do in the summary.
		for _, e := range doc.Events {
			res := e.Resources[0]
			if res.IaC != nil && (res.IaC.Tool != "terraform" || res.IaC.RepoURL != ocsfRepo || res.IaC.CommitSHA != ocsfCommit) {
				t.Errorf("iac: %+v", res.IaC)
			}
		}
	}
	// Without a repository or a commit, nothing of the iac source is left.
	doc := decodeOCSF(t, build(t, Options{Out: out, Document: DocOCSFInventory, DropAddresses: true}))
	for _, e := range doc.Events {
		if src := e.Resources[0].IaC; src != nil && *src != (iacSource{Tool: "terraform"}) {
			t.Errorf("iac: %+v", src)
		}
	}
}

// An address or a path that looks like a credential, as a key of a
// for_each can, stops the export, as an ID does; unless they're left out.
func TestOCSFInventoryRefusesCredentialAddress(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef-key-a")
	for name, tc := range map[string]struct {
		dir      string
		imported report.Resource
	}{
		"address": {ocsfDir, report.Resource{Address: `aws_s3_bucket.named["` + credentialTag + `"]`, ID: "other-bucket"}},
		"module":  {ocsfDir, report.Resource{Address: `module.` + credentialTag + `.aws_s3_bucket.x`, ID: "other-bucket"}},
		"path":    {"aws/" + credentialTag + "/eu-west-2", report.Resource{Address: "aws_s3_bucket.other", ID: "other-bucket"}},
	} {
		out := writeOCSFOut(t, tc.dir, tc.imported)
		for _, k := range [][]byte{nil, key} {
			if _, err := Build(Options{Out: out, Document: DocOCSFInventory, Now: fixedNow, HashKey: k}); err == nil {
				t.Errorf("%s: a %s with a credential's format was written (hashed: %t)", name, name, k != nil)
			}
		}
		content := build(t, Options{Out: out, Document: DocOCSFInventory, DropAddresses: true})
		if strings.Contains(string(content), credentialTag) {
			t.Errorf("%s: the credential reached the document:\n%s", name, content)
		}
	}
	// So does an ID, as in the inventory.
	out := writeOCSFOut(t, ocsfDir, report.Resource{Address: "aws_s3_bucket.other", ID: credentialTag})
	if _, err := Build(Options{Out: out, Document: DocOCSFInventory, Now: fixedNow}); err == nil {
		t.Error("an ID with a credential's format was written")
	}
}

func TestModuleAddressOf(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_s3_bucket.logs":                          "",
		`aws_s3_bucket.logs["a.b"]`:                   "",
		"aws_s3_bucket.logs[0]":                       "",
		"module.net.aws_vpc.main":                     "module.net",
		"module.net.aws_vpc.main[0]":                  "module.net",
		`module.net["a.b"].aws_vpc.main["c.d"]`:       `module.net["a.b"]`,
		"module.a.module.b.aws_vpc.main":              "module.a.module.b",
		`module.a[0].module.b["x"].aws_vpc.main`:      `module.a[0].module.b["x"]`,
		`module.a["q\"].x"].aws_vpc.main`:             `module.a["q\"].x"]`,
		"module.net":                                  "",
		"":                                            "",
		"data.aws_vpc.main":                           "",
		"module.net.data.aws_vpc.main":                "module.net.data",
		"module.net.module.sub.aws_s3_bucket.logs[1]": "module.net.module.sub",
	} {
		if got := moduleAddressOf(address); got != want {
			t.Errorf("%q: got %q, want %q", address, got, want)
		}
	}
}

func TestCheckRepo(t *testing.T) {
	t.Parallel()
	good := map[string]Options{
		"url and commit": {Document: DocOCSFInventory, RepoURL: ocsfRepo, CommitSHA: ocsfCommit},
		"url only":       {Document: DocOCSFInventory, RepoURL: "https://git.example.com/org/sub/repo.git"},
		"sha-256":        {Document: DocOCSFInventory, CommitSHA: strings.Repeat("ab", 32)},
		"plain http":     {Document: DocOCSFInventory, RepoURL: "http://ghes.internal/org/repo"},
		"neither":        {Document: DocInventory},
	}
	for name, opts := range good {
		if err := checkRepo(opts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]Options{
		"credentials":         {Document: DocOCSFInventory, RepoURL: "https://user:pw@github.com/o/r"},
		"token as user":       {Document: DocOCSFInventory, RepoURL: "https://" + credentialTag + "@github.com/o/r"},
		"token in the path":   {Document: DocOCSFInventory, RepoURL: "https://github.com/o/" + credentialTag},
		"query":               {Document: DocOCSFInventory, RepoURL: "https://github.com/o/r?token=x"},
		"fragment":            {Document: DocOCSFInventory, RepoURL: "https://github.com/o/r#x"},
		"not a URL":           {Document: DocOCSFInventory, RepoURL: "github.com/o/r"},
		"other scheme":        {Document: DocOCSFInventory, RepoURL: "ssh://git@github.com/o/r"},
		"no host":             {Document: DocOCSFInventory, RepoURL: "https:///o/r"},
		"short commit":        {Document: DocOCSFInventory, CommitSHA: "abc123"},
		"upper-case commit":   {Document: DocOCSFInventory, CommitSHA: strings.ToUpper(ocsfCommit)},
		"commit with a space": {Document: DocOCSFInventory, CommitSHA: ocsfCommit[:39] + " "},
		"url, inventory":      {Document: DocInventory, RepoURL: ocsfRepo},
		"commit, run":         {Document: DocRun, CommitSHA: ocsfCommit},
	}
	for name, opts := range bad {
		if err := checkRepo(opts); err == nil {
			t.Errorf("%s: passed", name)
		}
		if _, err := Build(opts); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

func TestOCSFSchemaIsClosed(t *testing.T) {
	t.Parallel()
	c, err := checkerOf(DocOCSFInventory)
	if err != nil {
		t.Fatal(err)
	}
	if c.items["events"] == nil {
		t.Fatalf("the events aren't checked on their own: %v", c.items)
	}
	good := build(t, Options{Out: writeOCSFOut(t, ocsfDir), Document: DocOCSFInventory, RepoURL: ocsfRepo, CommitSHA: ocsfCommit})
	// event 0 is the bucket's, with the iac source of an import.
	event := func(doc map[string]any) map[string]any { return doc["events"].([]any)[0].(map[string]any) }
	res := func(doc map[string]any) map[string]any { return event(doc)["resources"].([]any)[0].(map[string]any) }
	iac := func(doc map[string]any) map[string]any { return res(doc)["iac"].(map[string]any) }
	meta := func(doc map[string]any) map[string]any { return event(doc)["metadata"].(map[string]any) }
	cloud := func(doc map[string]any) map[string]any { return event(doc)["cloud"].(map[string]any) }
	redaction := func(doc map[string]any) map[string]any { return doc["redaction"].(map[string]any) }
	mutations := map[string]func(doc map[string]any){
		"none":                  func(map[string]any) {},
		"extra top-level field": func(doc map[string]any) { doc["x"] = 1.0 },
		"extra event field":     func(doc map[string]any) { event(doc)["unmapped"] = map[string]any{"password": "x"} },
		"extra resource field":  func(doc map[string]any) { res(doc)["data"] = map[string]any{"password": "x"} },
		"extra iac field":       func(doc map[string]any) { iac(doc)["state"] = "x" },
		"extra metadata field":  func(doc map[string]any) { meta(doc)["log_name"] = "x" },
		"extra cloud field":     func(doc map[string]any) { cloud(doc)["zone"] = "x" },
		"extra tag field":       func(doc map[string]any) { res(doc)["tags"].([]any)[0].(map[string]any)["values"] = []any{} },
		"no redaction":          func(doc map[string]any) { delete(doc, "redaction") },
		"redaction null":        func(doc map[string]any) { doc["redaction"] = nil },
		"extra redaction field": func(doc map[string]any) { redaction(doc)["key"] = "x" },
		"no hash algorithm":     func(doc map[string]any) { delete(redaction(doc), "hash_alg") },
		"other hash algorithm":  func(doc map[string]any) { redaction(doc)["hash_alg"] = "sha256" },
		"no hashed flag":        func(doc map[string]any) { delete(redaction(doc), "ids_hashed") },
		"hashed flag not bool":  func(doc map[string]any) { redaction(doc)["ids_hashed"] = "yes" },
		"no addresses flag":     func(doc map[string]any) { delete(redaction(doc), "addresses_dropped") },
		"addresses flag text":   func(doc map[string]any) { redaction(doc)["addresses_dropped"] = "yes" },
		"no dropped keys":       func(doc map[string]any) { delete(redaction(doc), "tag_keys_dropped") },
		"dropped keys null":     func(doc map[string]any) { redaction(doc)["tag_keys_dropped"] = nil },
		"empty dropped key":     func(doc map[string]any) { redaction(doc)["tag_keys_dropped"] = []any{""} },
		"all tags not bool":     func(doc map[string]any) { redaction(doc)["all_tags_dropped"] = 1.0 },
		"other schema":          func(doc map[string]any) { doc["schema"] = "infraharvest/ocsf-inventory/v2" },
		"other OCSF version":    func(doc map[string]any) { doc["ocsf_version"] = "1.8.0" },
		"no OCSF version":       func(doc map[string]any) { delete(doc, "ocsf_version") },
		"run ID not a UUID":     func(doc map[string]any) { doc["run_id"] = "x" },
		"events null":           func(doc map[string]any) { doc["events"] = nil },
		"events an object":      func(doc map[string]any) { doc["events"] = map[string]any{} },
		"no events":             func(doc map[string]any) { delete(doc, "events") },
		"event not an object":   func(doc map[string]any) { doc["events"].([]any)[0] = "x" },
		"other class":           func(doc map[string]any) { event(doc)["class_uid"] = 5001.0 },
		"other category":        func(doc map[string]any) { event(doc)["category_uid"] = 4.0 },
		"other activity":        func(doc map[string]any) { event(doc)["activity_id"] = 1.0 },
		"type of another class": func(doc map[string]any) { event(doc)["type_uid"] = 502202.0 },
		"no severity":           func(doc map[string]any) { delete(event(doc), "severity_id") },
		"no time":               func(doc map[string]any) { delete(event(doc), "time") },
		"time a fraction":       func(doc map[string]any) { event(doc)["time"] = 1.5 },
		"time negative":         func(doc map[string]any) { event(doc)["time"] = -1.0 },
		"no metadata":           func(doc map[string]any) { delete(event(doc), "metadata") },
		"no metadata version":   func(doc map[string]any) { delete(meta(doc), "version") },
		"no product":            func(doc map[string]any) { delete(meta(doc), "product") },
		"other profile":         func(doc map[string]any) { meta(doc)["profiles"] = []any{"cloud", "host"} },
		"no extension":          func(doc map[string]any) { meta(doc)["extensions"] = []any{} },
		"other extension uid":   func(doc map[string]any) { meta(doc)["extensions"].([]any)[0].(map[string]any)["uid"] = "999" },
		"no cloud":              func(doc map[string]any) { delete(event(doc), "cloud") },
		"no provider":           func(doc map[string]any) { delete(cloud(doc), "provider") },
		"other provider":        func(doc map[string]any) { cloud(doc)["provider"] = "Oracle" },
		"empty account":         func(doc map[string]any) { cloud(doc)["account"] = map[string]any{"uid": ""} },
		"no resources":          func(doc map[string]any) { event(doc)["resources"] = []any{} },
		"resources null":        func(doc map[string]any) { event(doc)["resources"] = nil },
		"no uid":                func(doc map[string]any) { delete(res(doc), "uid") },
		"empty uid":             func(doc map[string]any) { res(doc)["uid"] = "" },
		"no type":               func(doc map[string]any) { delete(res(doc), "type") },
		"tag without a name":    func(doc map[string]any) { delete(res(doc)["tags"].([]any)[0].(map[string]any), "name") },
		"tag not text":          func(doc map[string]any) { res(doc)["tags"].([]any)[0].(map[string]any)["value"] = 1.0 },
		"duplicate label":       func(doc map[string]any) { res(doc)["labels"] = []any{"a", "a"} },
		"other tool":            func(doc map[string]any) { iac(doc)["tool"] = "pulumi" },
		"empty iac":             func(doc map[string]any) { res(doc)["iac"] = map[string]any{} },
		"empty address":         func(doc map[string]any) { iac(doc)["address"] = "" },
		"commit too short":      func(doc map[string]any) { iac(doc)["commit_sha"] = "abc" },
		"commit upper case":     func(doc map[string]any) { iac(doc)["commit_sha"] = strings.ToUpper(ocsfCommit) },
		"repository credential": func(doc map[string]any) { iac(doc)["repo_url"] = "https://user:pw@github.com/o/r" },
		"repository not a URL":  func(doc map[string]any) { iac(doc)["repo_url"] = "github.com/o/r" },
		"repository query":      func(doc map[string]any) { iac(doc)["repo_url"] = "https://github.com/o/r?x=1" },
		"workspace not text":    func(doc map[string]any) { iac(doc)["workspace"] = 1.0 },
	}
	for name, mutate := range mutations {
		var doc map[string]any
		if err := json.Unmarshal(good, &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc)
		content, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var instance any
		if err := json.Unmarshal(content, &instance); err != nil {
			t.Fatal(err)
		}
		whole, split := c.whole.Validate(instance), validate(DocOCSFInventory, content)
		if (whole == nil) != (split == nil) {
			t.Errorf("%s: the whole schema says %v, the check %v", name, whole, split)
		}
		if name != "none" && split == nil {
			t.Errorf("%s: passed", name)
		}
		if name == "none" && split != nil {
			t.Errorf("a good document failed: %v", split)
		}
	}
}

// The values of the document's extension and profile are those of the
// extension in schemas/ocsf/iac, which declares them.
func TestOCSFExtensionFilesAgreeWithTheDocument(t *testing.T) {
	t.Parallel()
	read := func(path string) map[string]any {
		content, err := os.ReadFile(filepath.Join("..", "..", "schemas", "ocsf", "iac", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(content, &v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return v
	}
	ext := read("extension.json")
	if ext["name"] != ocsfExtension || ext["version"] != ocsfExtensionVersion || ext["uid"] != 1301.0 || ocsfExtensionUID != "1301" {
		t.Errorf("extension.json: %v", ext)
	}
	profile := read("profiles/iac_source.json")
	if ocsfProfileIaC != ext["name"].(string)+"/"+profile["name"].(string) || profile["meta"] != "profile" {
		t.Errorf("profile: %v", profile)
	}
	// The object's attributes are the fields iacSource encodes, and the
	// dictionary defines each of them but path, which OCSF has.
	object := read("objects/iac_source.json")["attributes"].(map[string]any)
	dictionary := read("dictionary.json")["attributes"].(map[string]any)
	want := []string{"tool", "address", "module_address", "repo_url", "commit_sha", "path", "workspace", "state_key"}
	if got := slices.Sorted(maps.Keys(object)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("object attributes: %v, want %v", got, want)
	}
	for _, name := range want {
		if _, ok := dictionary[name]; !ok && name != "path" {
			t.Errorf("%s isn't in the dictionary", name)
		}
	}
	if _, ok := dictionary["path"]; ok {
		t.Error("the dictionary redefines OCSF's path")
	}
	patch := read("objects/resource_details.json")
	if patch["extends"] != "resource_details" || patch["profiles"].([]any)[0] != ocsfProfileIaC {
		t.Errorf("resource_details: %v", patch)
	}
	if _, ok := patch["attributes"].(map[string]any)["iac"]; !ok {
		t.Error("resource_details doesn't get the iac attribute")
	}
	// The fields the Go type encodes are the object's.
	encoded := roundTrip(t, func() any { var s iacSource; fill(reflect.ValueOf(&s)); return &s }()).(map[string]any)
	if got := slices.Sorted(maps.Keys(encoded)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("iacSource encodes %v, want %v", got, want)
	}
}

// The document records its redaction as the inventory does, so that a
// receiver can tell a hashed ID from a real one.
func TestOCSFInventoryRedaction(t *testing.T) {
	t.Parallel()
	out := writeOCSFOut(t, ocsfDir)
	key := []byte("0123456789abcdef-key-a")
	opts := func(o Options) Options {
		o.Out, o.Document = out, DocOCSFInventory
		return o
	}

	plain := decodeOCSF(t, build(t, opts(Options{})))
	// Tags that look like secrets are dropped and listed, as in the
	// inventory, and the list is there, though it may be empty.
	if got := strings.Join(plain.Redaction.TagKeysDropped, ","); got != secretTagKey+",owner-note" {
		t.Errorf("tag keys dropped: %q", got)
	}
	if want := (ocsfRedaction{TagKeysDropped: plain.Redaction.TagKeysDropped, HashAlg: hashAlg}); !reflect.DeepEqual(plain.Redaction, want) {
		t.Errorf("redaction: %+v, want %+v", plain.Redaction, want)
	}
	inv := decodeInventory(t, build(t, Options{Out: out, Document: DocInventory}))
	if !slices.Equal(plain.Redaction.TagKeysDropped, inv.Redaction.TagKeysDropped) {
		t.Errorf("the OCSF inventory dropped %v, the inventory %v", plain.Redaction.TagKeysDropped, inv.Redaction.TagKeysDropped)
	}
	if strings.Contains(string(build(t, opts(Options{}))), `"all_tags_dropped"`) {
		t.Error("all_tags_dropped is there without --drop-tags")
	}

	one := decodeOCSF(t, build(t, opts(Options{DropTags: []string{keepTagKey}})))
	if got := strings.Join(one.Redaction.TagKeysDropped, ","); got != secretTagKey+",owner-note,"+keepTagKey {
		t.Errorf("--drop-tag: %q", got)
	}
	all := decodeOCSF(t, build(t, opts(Options{DropAllTags: true})))
	if !all.Redaction.AllTagsDropped || all.Redaction.IDsHashed || all.Redaction.AddressesDropped {
		t.Errorf("--drop-tags: %+v", all.Redaction)
	}

	hashed := decodeOCSF(t, build(t, opts(Options{HashKey: key})))
	if !hashed.Redaction.IDsHashed || hashed.Redaction.HashAlg != "hmac-sha256" || hashed.Redaction.AddressesDropped {
		t.Errorf("--hash-ids: %+v", hashed.Redaction)
	}
	// The key never goes into the document.
	if strings.Contains(string(build(t, opts(Options{HashKey: key}))), string(key)) {
		t.Error("the document holds the key")
	}

	dropped := decodeOCSF(t, build(t, opts(Options{DropAddresses: true})))
	if !dropped.Redaction.AddressesDropped || dropped.Redaction.IDsHashed {
		t.Errorf("--drop-addresses: %+v", dropped.Redaction)
	}
	both := decodeOCSF(t, build(t, opts(Options{DropAddresses: true, HashKey: key})))
	if !both.Redaction.AddressesDropped || !both.Redaction.IDsHashed {
		t.Errorf("both: %+v", both.Redaction)
	}
}
