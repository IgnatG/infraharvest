// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/drift"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// historyProvider is an aws provider with an audit log: records, by ID,
// and what it was asked.
type historyProvider struct {
	fakeProvider
	records map[string]harvest.ChangeRecord
	err     error
	mu      sync.Mutex
	asked   []map[string]string
	ids     [][]string
}

func (*historyProvider) Name() string { return "aws" }

func (p *historyProvider) LastChanges(_ context.Context, settings map[string]string, ids []string) (map[string]harvest.ChangeRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, settings)
	p.ids = append(p.ids, ids)
	found := map[string]harvest.ChangeRecord{}
	for _, id := range ids {
		if r, ok := p.records[id]; ok {
			found[id] = r
		}
	}
	return found, p.err
}

// historyOutput writes an output directory with a root at rel whose
// providers.tf configures aws in eu-west-2, and an aliased aws provider
// elsewhere, which isn't the one asked.
func historyOutput(t *testing.T, rel string) string {
	t.Helper()
	out := t.TempDir()
	path := filepath.Join(out, filepath.FromSlash(rel), rootfile.ProvidersFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	providers := "provider \"aws\" {\n  alias  = \"other\"\n  region = \"us-west-1\"\n}\n\n" +
		"provider \"aws\" {\n  region              = \"eu-west-2\"\n  allowed_account_ids = [\"123456789012\"]\n}\n"
	if err := os.WriteFile(path, []byte(providers), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDriftLastChange(t *testing.T) {
	t.Parallel()
	rel := "aws/123456789012/eu-west-2"
	out := historyOutput(t, rel)
	at := time.Date(2026, 11, 8, 14, 3, 0, 0, time.UTC)
	p := &historyProvider{records: map[string]harvest.ChangeRecord{
		// Found by its ARN, the second of its IDs.
		"arn:aws:s3:::logs": {By: "alice", Call: "PutBucketTagging", At: at, Log: "CloudTrail"},
	}}
	f := &fakeDriftCheck{out: out, results: map[string]*drift.Result{rel: {Backend: "s3", Resources: 2, Changes: []drift.Change{
		{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: drift.Changed, Outcome: drift.Kept, IDs: []string{"logs", "arn:aws:s3:::logs"}},
		{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Team", Before: "a", After: "b", Kind: drift.Changed, Outcome: drift.Kept, IDs: []string{"logs", "arn:aws:s3:::logs"}},
		{Address: "aws_sqs_queue.jobs", Type: "aws_sqs_queue", Attribute: "delay_seconds", Before: 0.0, After: 5.0, Kind: drift.Changed, Outcome: drift.Kept, IDs: []string{"https://sqs/jobs"}},
		{Address: "local.tags", Attribute: "tags.Env", Kind: drift.Changed, Outcome: drift.Kept, Covers: []string{"aws_vpc.main"}},
	}}}}
	options := driftOptions(out, f)
	options.Provider = func(name string) harvest.Provider {
		if name == "aws" {
			return p
		}
		return nil
	}
	options.Names = []string{"aws"}
	options.LastChange = true
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	// Asked once, in the region of the unaliased provider block, for each
	// resource's IDs once.
	if len(p.asked) != 1 || p.asked[0]["region"] != "eu-west-2" {
		t.Errorf("settings %v, want the unaliased block's region", p.asked)
	}
	if want := [][]string{{"logs", "arn:aws:s3:::logs", "https://sqs/jobs"}}; !reflect.DeepEqual(p.ids, want) {
		t.Errorf("IDs %v, want %v", p.ids, want)
	}
	d := readDrift(t, out)
	wantChange := report.DriftLastChange{By: "alice", Call: "PutBucketTagging", At: "2026-11-08T14:03:00Z", Log: "CloudTrail"}
	for _, c := range d.Changes {
		switch c.Address {
		case "aws_s3_bucket.logs":
			if c.LastChange == nil || *c.LastChange != wantChange {
				t.Errorf("%s %s: %+v, want %+v", c.Address, c.Attribute, c.LastChange, wantChange)
			}
		default:
			if c.LastChange != nil {
				t.Errorf("%s: %+v, want none", c.Address, c.LastChange)
			}
		}
	}
	md, err := os.ReadFile(filepath.Join(out, report.Dir, report.DriftMarkdownFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "last changed by `alice` with `PutBucketTagging` at 2026-11-08T14:03:00Z (CloudTrail)"; !strings.Contains(string(md), want) {
		t.Errorf("drift.md doesn't say %q:\n%s", want, md)
	}

	// Without --last-change, the log isn't read.
	p.asked = nil
	options.LastChange = false
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 0 {
		t.Error("read the audit log without --last-change")
	}
}

// A log that can't be read leaves the drift run as it is, with what was
// found before it.
func TestDriftLastChangeFailureIsNotFatal(t *testing.T) {
	t.Parallel()
	rel := "aws/123456789012/eu-west-2"
	out := historyOutput(t, rel)
	p := &historyProvider{
		records: map[string]harvest.ChangeRecord{"logs": {By: "alice", Call: "PutBucketTagging", At: time.Now(), Log: "CloudTrail"}},
		err:     errors.New("AccessDeniedException"),
	}
	f := &fakeDriftCheck{out: out, results: map[string]*drift.Result{rel: {Backend: "s3", Resources: 1, Changes: []drift.Change{
		{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: drift.Changed, Outcome: drift.Kept, IDs: []string{"logs"}},
	}}}}
	options := driftOptions(out, f)
	options.Provider = func(string) harvest.Provider { return p }
	options.Names = []string{"aws"}
	options.LastChange = true
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if d := readDrift(t, out); d.Changes[0].LastChange == nil {
		t.Error("what was found before the failure was dropped")
	}
}

func TestProviderSettings(t *testing.T) {
	t.Parallel()
	out := historyOutput(t, "root")
	dir := filepath.Join(out, "root")
	if got := providerSettings(dir, "aws"); got["region"] != "eu-west-2" || len(got) != 1 {
		t.Errorf("settings %v: want the region alone (allowed_account_ids is no string)", got)
	}
	if got := providerSettings(dir, "google"); got != nil {
		t.Errorf("another provider's settings: %v", got)
	}
	if got := providerSettings(t.TempDir(), "aws"); got != nil {
		t.Errorf("without providers.tf: %v", got)
	}
}
