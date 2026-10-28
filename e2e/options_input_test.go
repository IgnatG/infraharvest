// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/report"
)

// optionsSkipReason is the reason of the skip rule in
// testdata/catalogue/skip, and optionsLegacyTopic the topic it leaves out.
const (
	optionsSkipReason  = "retired, per the end-to-end tests' catalogue overlay"
	optionsLegacyTopic = "arn:aws:sns:us-east-1:" + optionsAccount + ":infraharvest-e2e-options-legacy"
)

// optionsCatalogue returns the directory of the catalogue overlay
// testdata/catalogue/<name>.
func optionsCatalogue(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "catalogue", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// sortedIDs returns ids sorted, as optionsImports lists them.
func sortedIDs(ids ...string) []string {
	sort.Strings(ids)
	return ids
}

// TestAWSOptionsReport checks the report options, the report command and
// what the default selection leaves out. With --format json the import
// prints its whole report on stdout as one JSON document, with the
// documented fields, which infraharvest report --format json prints
// again, and infraharvest report prints report.md; --report-tags counts
// the resources by the value of the tag; --list-parallel=1 lists one
// service at a time; the skip rule of a --catalogue overlay leaves the
// legacy topic out; the queue tagged ManagedBy=Terraform is left out as
// managed elsewhere, and imported with --include-owned. Every root plans
// as imports with no changes.
func TestAWSOptionsReport(t *testing.T) {
	e := seedOptions(t)
	services := []string{"sqs", "sns", "ebs"}
	out, stdout, logged := e.importAWS(t, report.ExitOK, "--services="+strings.Join(services, ","), "--format=json", "--report-tags=team", "--list-parallel=1", "--catalogue="+optionsCatalogue(t, "skip"))

	var printed map[string]any
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&printed); err != nil {
		t.Fatalf("--format json printed no JSON document on stdout: %v\n%s", err, stdout)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Errorf("--format json printed more than one JSON document on stdout (%v):\n%s", err, stdout)
	}
	for _, key := range []string{"schema_version", "tool", "engine", "provider", "types", "directories", "excluded", "totals", "tags", "exit_code"} {
		if _, ok := printed[key]; !ok {
			t.Errorf("the JSON report has no %q:\n%s", key, stdout)
		}
	}
	if printed["schema_version"] != float64(report.SchemaVersion) || printed["exit_code"] != float64(report.ExitOK) {
		t.Errorf("the JSON report has schema_version %v and exit_code %v, want %d and %d", printed["schema_version"], printed["exit_code"], report.SchemaVersion, report.ExitOK)
	}
	again, _, err := optionsRun(e.ctx, t, "report", "--format=json", out)
	if err != nil {
		t.Fatalf("infraharvest report --format json: %v", err)
	}
	var reread map[string]any
	if err := json.Unmarshal([]byte(again), &reread); err != nil {
		t.Fatalf("infraharvest report --format json printed no JSON document: %v\n%s", err, again)
	}
	if !reflect.DeepEqual(printed, reread) {
		t.Errorf("infraharvest report --format json prints another report than import --format json did:\n%s\n---\n%s", again, stdout)
	}
	text, _, err := optionsRun(e.ctx, t, "report", out)
	if err != nil {
		t.Fatalf("infraharvest report: %v", err)
	}
	if markdown := readFile(t, filepath.Join(out, report.Dir, "report.md")); text != markdown {
		t.Errorf("infraharvest report prints another report than report.md:\n%s\n---\n%s", text, markdown)
	}

	r := optionsReport(t, out)
	byValue := map[string]report.TagCount{}
	for _, c := range r.Tags {
		if c.Key != "team" {
			t.Errorf("the report counts by the tag %s, which --report-tags doesn't name", c.Key)
		}
		byValue[c.Value] = c
	}
	if web := byValue["web"]; web.Discovered == 0 || web.Imported == 0 {
		t.Errorf("--report-tags=team: team=web counts %+v, want the orders queue imported; counts %+v", web, r.Tags)
	}
	if platform := byValue["platform"]; platform.Discovered != 1 || platform.Excluded != 1 || platform.Managed != 1 {
		t.Errorf("--report-tags=team: team=platform counts %+v, want the queue another state manages; counts %+v", platform, r.Tags)
	}
	if !strings.Contains(text, "## By tag") {
		t.Errorf("--report-tags=team: report.md has no counts by tag:\n%s", text)
	}

	// The services list one at a time, in the log from "aws: listing <service>"
	// to "aws: listed <service>".
	listing, started := "", 0
	for _, entry := range logged.entries {
		for _, service := range services {
			switch {
			case strings.HasSuffix(entry.message, "aws: listing "+service):
				started++
				if listing != "" {
					t.Errorf("--list-parallel=1: %s started listing while %s was", service, listing)
				}
				listing = service
			case strings.HasSuffix(entry.message, "aws: listed "+service) && listing == service:
				listing = ""
			}
		}
	}
	if started != len(services) {
		t.Errorf("--list-parallel=1: the log shows %d services listing, want %d", started, len(services))
	}

	if legacy := e.id(t, "aws_sns_topic", "legacy"); legacy != optionsLegacyTopic {
		t.Fatalf("testdata/catalogue/skip names the legacy topic %s, but the emulator gave it %s", optionsLegacyTopic, legacy)
	}
	reasons := map[string]string{}
	for _, x := range r.Excluded {
		reasons[x.Type+" "+x.ID] = x.Reason
	}
	if reason := reasons["aws_sns_topic "+optionsLegacyTopic]; reason != optionsSkipReason {
		t.Errorf("--catalogue: the legacy topic is left out with %q, want the overlay's %q; excluded %+v", reason, optionsSkipReason, r.Excluded)
	}
	owned := e.id(t, "aws_sqs_queue", "owned")
	if reason := reasons["aws_sqs_queue "+owned]; !strings.HasPrefix(reason, report.TerraformPrefix) {
		t.Errorf("the queue tagged ManagedBy=Terraform is left out with %q, want %q; excluded %+v", reason, report.TerraformPrefix, r.Excluded)
	}
	orders, jobs := e.id(t, "aws_sqs_queue", "orders"), e.id(t, "aws_sqs_queue", "jobs")
	want := map[string][]string{
		"aws_sqs_queue":  sortedIDs(orders, jobs),
		"aws_sns_topic":  {e.id(t, "aws_sns_topic", "events")},
		"aws_ebs_volume": sortedIDs(e.id(t, "aws_ebs_volume", "web"), e.id(t, "aws_ebs_volume", "data"), e.id(t, "aws_ebs_volume", "scratch")),
	}
	if got := optionsImports(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("the roots import %v, want %v", got, want)
	}
	e.planAll(t, out)

	withOwned, _, _ := e.importAWS(t, report.ExitOK, "--services=sqs", "--include-owned")
	if got, want := optionsImports(t, withOwned)["aws_sqs_queue"], sortedIDs(orders, jobs, owned); !slices.Equal(got, want) {
		t.Errorf("--include-owned: the roots import the queues %v, want %v", got, want)
	}
	for _, x := range optionsReport(t, withOwned).Excluded {
		if x.ID == owned {
			t.Errorf("--include-owned: the queue tagged ManagedBy=Terraform is left out: %s", x.Reason)
		}
	}
	if imported := e.planAll(t, withOwned); imported["aws_sqs_queue"] != 3 {
		t.Errorf("--include-owned: the roots import %v, want the three queues", imported)
	}
}

// TestAWSOptionsFilters checks --services, --skip, --id, --tag and
// --where: each import has exactly what its filters keep. The EBS lister
// passes --tag and --where on tags to DescribeVolumes, with a value or
// with the key only; the SQS lister, which doesn't support them, lists
// every queue but the one the default selection leaves out as managed
// elsewhere, and --id keeps the queue it names. Every root plans as
// imports with no changes.
func TestAWSOptionsFilters(t *testing.T) {
	e := seedOptions(t)
	web, data := e.id(t, "aws_ebs_volume", "web"), e.id(t, "aws_ebs_volume", "data")
	orders, jobs := e.id(t, "aws_sqs_queue", "orders"), e.id(t, "aws_sqs_queue", "jobs")
	for _, c := range []struct {
		flags []string
		want  map[string][]string
	}{
		{
			flags: []string{"--services=sqs,sns,ebs", "--skip=sns", "--tag=team=data"},
			want:  map[string][]string{"aws_ebs_volume": {data}, "aws_sqs_queue": sortedIDs(orders, jobs)},
		},
		{
			flags: []string{"--services=sqs,ebs", "--where=tags.team=web", "--id=sqs_queue=" + orders},
			want:  map[string][]string{"aws_ebs_volume": {web}, "aws_sqs_queue": {orders}},
		},
		{
			flags: []string{"--services=ebs", "--tag=team"},
			want:  map[string][]string{"aws_ebs_volume": sortedIDs(web, data)},
		},
	} {
		flags := strings.Join(c.flags, " ")
		out, _, _ := e.importAWS(t, report.ExitOK, c.flags...)
		if got := optionsImports(t, out); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: the roots import %v, want %v", flags, got, c.want)
		}
		imported := e.planAll(t, out)
		for typ, ids := range c.want {
			if imported[typ] != len(ids) {
				t.Errorf("%s: the roots plan to import %d %s, want %d", flags, imported[typ], typ, len(ids))
			}
		}
	}
}

// TestAWSOptionsExitCodes checks the exit codes of a partial import,
// which a --catalogue overlay makes partial by giving the topics an
// import ID that names none: 1 without --allow-partial and 3 with it,
// with the topics left out into rejected.hcl and the queues in a root
// that plans as imports with no changes. And that --engine-timeout fails
// a root whose Terraform command takes longer: with 1ms, every root.
func TestAWSOptionsExitCodes(t *testing.T) {
	e := seedOptions(t)
	gone := optionsCatalogue(t, "gone")
	topics := sortedIDs(e.id(t, "aws_sns_topic", "events")+"-gone", e.id(t, "aws_sns_topic", "legacy")+"-gone")
	queues := sortedIDs(e.id(t, "aws_sqs_queue", "orders"), e.id(t, "aws_sqs_queue", "jobs"))
	for _, c := range []struct {
		flags []string
		want  int
	}{
		{nil, report.ExitIncomplete},
		{[]string{"--allow-partial"}, report.ExitPartial},
	} {
		flags := strings.Join(append([]string{"--catalogue=testdata/catalogue/gone"}, c.flags...), " ")
		out, _, _ := e.importAWS(t, c.want, append([]string{"--services=sqs,sns", "--catalogue=" + gone}, c.flags...)...)
		r := optionsReport(t, out)
		if r.ExitCode != c.want {
			t.Errorf("%s: the report says exit code %d, want %d", flags, r.ExitCode, c.want)
		}
		var leftOut []string
		for _, d := range r.Directories {
			for _, l := range d.LeftOut {
				if !strings.HasPrefix(l.Address, "aws_sns_topic.") {
					t.Errorf("%s: %s is left out: %v", flags, l.Address, l.Errors)
				}
				leftOut = append(leftOut, l.ID)
			}
		}
		sort.Strings(leftOut)
		if !slices.Equal(leftOut, topics) || r.Totals.LeftOut != len(topics) {
			t.Errorf("%s: left out %v (totals %+v), want the topics %v", flags, leftOut, r.Totals, topics)
		}
		dir := filepath.Join(out, filepath.FromSlash(optionsRoot))
		if rejected, err := os.ReadFile(filepath.Join(dir, rootfile.RejectedFileName)); err != nil || !strings.Contains(string(rejected), "aws_sns_topic") {
			t.Errorf("%s: %s doesn't hold the topics (%v):\n%s", flags, rootfile.RejectedFileName, err, rejected)
		}
		if got, want := optionsImports(t, out), map[string][]string{"aws_sqs_queue": queues}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the roots import %v, want %v", flags, got, want)
		}
		if imported := checkNoChanges(e.ctx, t, dir, e.execPath, e.pluginCache, nil); imported["aws_sqs_queue"] != len(queues) || len(imported) != 1 {
			t.Errorf("%s: %s plans to import %v, want the queues", flags, optionsRoot, imported)
		}
	}

	out, _, _ := e.importAWS(t, report.ExitIncomplete, "--services=sqs", "--engine-timeout=1ms")
	r := optionsReport(t, out)
	if len(r.Directories) != 1 || !strings.Contains(r.Directories[0].Error, "timed out after 1ms") || r.Totals.Imported != 0 || r.Totals.Failed == 0 {
		t.Errorf("--engine-timeout=1ms: totals %+v, directories %+v, want the root failed on the timeout", r.Totals, r.Directories)
	}
}
