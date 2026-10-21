// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// noEnv places no resource in an environment.
func noEnv(harvest.Resource) string { return "" }

func TestEnvPathPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		tags    []string
		want    string
	}{
		{DefaultLayout, nil, DefaultLayout},
		{DefaultLayout, []string{"Environment"}, envLayout},
		{"", []string{"Environment"}, envLayout},
		{"{out}/{env}/{provider}/", nil, "{out}/{env}/{provider}/"},
		// A layout of the user's own isn't split behind their back.
		{"{out}/{provider}/{service}/", []string{"Environment"}, "{out}/{provider}/{service}/"},
	} {
		if got := withEnvLayout(tc.pattern, len(tc.tags) > 0); got != tc.want {
			t.Errorf("withEnvLayout(%q, %v) = %q, want %q", tc.pattern, tc.tags, got, tc.want)
		}
	}
}

func TestEnvironments(t *testing.T) {
	bucket := harvest.RecordBare("logs-prod", "logs", "aws_s3_bucket", "aws")
	versioning := harvest.RecordBare("logs-prod", "logs", "aws_s3_bucket_versioning", "aws")
	subnet := harvest.RecordBare("subnet-0abc1234", "app", "aws_subnet", "aws")
	association := harvest.RecordBare("rtbassoc-1", "app", "aws_route_table_association", "aws")
	vpc := harvest.RecordBare("vpc-0abc1234", "main", "aws_vpc", "aws")
	child := harvest.RecordBare("child-1", "child", "aws_lb_listener_rule", "aws")
	resources := map[string][]harvest.Resource{
		"s3":     {bucket, versioning},
		"subnet": {subnet},
		"route":  {association},
		"vpc":    {vpc, child},
	}
	tags := map[string]map[string]string{
		tagKey(bucket): {"Environment": "Prod"},
		tagKey(subnet): {"env": "Staging EU"},
	}
	importID := func(r harvest.Resource) (string, bool) {
		if r.Address.Type == "aws_route_table_association" {
			return "subnet-0abc1234/rtb-0def5678", true
		}
		return r.Observed.ID, true
	}
	parents := map[string]string{tagKey(child): tagKey(bucket)}

	got := environments(resources, tags, byTagKeys(defaultEnvTags), parents, importID, untaggedEnv(""))
	want := map[string]string{
		tagKey(bucket):      "prod",
		tagKey(versioning):  "prod",       // the bucket's ID is its import ID
		tagKey(subnet):      "staging-eu", // a directory name
		tagKey(association): "staging-eu", // the subnet's ID is in its import ID
		tagKey(vpc):         sharedEnv,
		tagKey(child):       "prod", // its parent's
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// With --default-env, what nothing places goes to that environment,
	// named as a tag value would be; the rest is placed as before.
	got = environments(resources, tags, byTagKeys(defaultEnvTags), parents, importID, untaggedEnv("Dev"))
	want[tagKey(vpc)] = "dev"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("--default-env: got %v, want %v", got, want)
	}
}

// Each root's resources in other roots are external to it, those a data
// source can read.
func TestOtherRoots(t *testing.T) {
	byDir := map[string][]engine.Import{
		"envs/prod":    {{Type: "aws_s3_bucket", ID: "logs-prod"}},
		"envs/staging": {{Type: "aws_s3_bucket", ID: "logs-staging"}, {Type: "aws_sqs_queue", ID: "https://sqs/jobs"}, {Type: "aws_s3_bucket_versioning", ID: "logs-staging"}},
	}
	opts := engine.Options{
		External: []engine.External{{Type: "aws_vpc", ID: "vpc-1"}},
		DataSources: map[string]engine.DataSource{
			"aws_s3_bucket": {Type: "aws_s3_bucket", Argument: "bucket"},
			"aws_sqs_queue": {Type: "aws_sqs_queue", Argument: "name"},
		},
	}
	dirOpts := newOtherRoots(opts, byDir, "").of("envs/prod")
	want := []engine.External{{Type: "aws_s3_bucket", ID: "logs-staging"}, {Type: "aws_sqs_queue", ID: "https://sqs/jobs"}, {Type: "aws_vpc", ID: "vpc-1"}}
	if got := dirOpts.External; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// And where they are, for the variables of what no data source reads.
	wantRoots := []engine.RootResource{
		{Type: "aws_s3_bucket", ID: "logs-staging", Name: "resource", Root: "envs/staging"},
		{Type: "aws_s3_bucket_versioning", ID: "logs-staging", Name: "resource", Root: "envs/staging"},
		{Type: "aws_sqs_queue", ID: "https://sqs/jobs", Name: "resource", Root: "envs/staging"},
	}
	if got := dirOpts.OtherRoots; !reflect.DeepEqual(got, wantRoots) {
		t.Errorf("other roots: got %v, want %v", got, wantRoots)
	}
	if len(opts.External) != 1 {
		t.Error("opts changed")
	}
	if one := newOtherRoots(opts, map[string][]engine.Import{"a": nil}, "").of("a"); len(one.External) != 1 {
		t.Errorf("one root: %v", one.External)
	}
}

func TestImportsByDirPerEnvironment(t *testing.T) {
	prod := harvest.RecordBare("logs-prod", "logs", "aws_s3_bucket", "aws")
	shared := harvest.RecordBare("vpc-1", "main", "aws_vpc", "aws")
	options := Options{Layout: "{out}/envs/{env}/{provider}/", Out: "out"}
	envOf := func(r harvest.Resource) string {
		if r.Address.Type == "aws_vpc" {
			return sharedEnv
		}
		return "prod"
	}
	got, _ := importsByDir("aws", map[string][]harvest.Resource{"s3": {prod}, "vpc": {shared}}, listerID, layoutDirs(options.Layout, options.Out, "aws", envOf), rawName, noFile)
	if len(got[filepath.FromSlash("out/envs/prod/aws")]) != 1 || len(got[filepath.FromSlash("out/envs/shared/aws")]) != 1 {
		t.Errorf("roots: %v", got)
	}
}

func rawName(r harvest.Resource) string      { return r.RawName }
func noFile(string, harvest.Resource) string { return "" }

// The configuration file's environments place a scope's resources: all
// in its one environment, or by the tags of those that share it, as
// --env-tags does, with what no tags place in sharedEnv.
func TestConfiguredEnvOf(t *testing.T) {
	bucket := harvest.RecordBare("shop-prd-logs", "logs", "aws_s3_bucket", "aws")
	versioning := harvest.RecordBare("shop-prd-logs", "logs", "aws_s3_bucket_versioning", "aws")
	queue := harvest.RecordBare("https://sqs/shop-dev-jobs", "jobs", "aws_sqs_queue", "aws")
	vpc := harvest.RecordBare("vpc-1", "main", "aws_vpc", "aws")
	resources := map[string][]harvest.Resource{"s3": {bucket, versioning}, "sqs": {queue}, "vpc": {vpc}}
	tags := map[string]map[string]string{
		tagKey(bucket): {"Environment": "Prod"},
		tagKey(queue):  {"Environment": "dev", "Team": "shop"},
		tagKey(vpc):    {"Environment": "shared-services"},
	}
	envs := config.Environments{
		"dev":  {AWS: &config.AWSScopes{Accounts: []string{"111111111111"}}, Tags: map[string]string{"Environment": "dev"}},
		"prod": {AWS: &config.AWSScopes{Accounts: []string{"111111111111", "222222222222"}}, Tags: map[string]string{"Environment": "prod"}},
	}
	for _, tc := range []struct {
		name  string
		names []string
		want  map[harvest.Resource]string
	}{
		{"one environment", []string{"prod"}, map[harvest.Resource]string{bucket: "prod", versioning: "prod", queue: "prod", vpc: "prod"}},
		{"split by tags", []string{"dev", "prod"}, map[harvest.Resource]string{
			bucket:     "prod",
			versioning: "prod", // the bucket's ID is its import ID
			queue:      "dev",
			vpc:        sharedEnv, // no environment's tags
		}},
	} {
		envOf := configuredEnvOf(envs, tc.names, resources, tags, nil, listerID)
		for r, want := range tc.want {
			if got := envOf(r); got != want {
				t.Errorf("%s: %s: got %q, want %q", tc.name, r.Address.Type, got, want)
			}
		}
	}
}

func TestScopeAccount(t *testing.T) {
	for scope, want := range map[string]string{
		"aws/111111111111/eu-west-2": "111111111111",
		"azure/0b1c2d3e/all":         "0b1c2d3e",
		"google/shop-prod/global":    "shop-prod",
		"":                           "",
		"aws/111111111111":           "",
	} {
		if got := scopeAccount(scope); got != want {
			t.Errorf("scopeAccount(%q) = %q, want %q", scope, got, want)
		}
	}
}

// A root an earlier import generated keeps its directory: what the
// environments would put elsewhere stays in it, and the report names it.
// Where nothing was generated, or the environment's root was, the
// environments lay the roots out.
func TestKeptRoots(t *testing.T) {
	out := t.TempDir()
	old := filepath.Join(out, "aws", "111111111111", "us-east-1")
	split := filepath.Join(out, "envs", "dev", "aws", "333333333333", "us-east-1")
	for _, dir := range []string{old, split, filepath.Join(out, "aws", "333333333333", "us-east-1")} {
		path := checkpointPath(out, dir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	account := func(r harvest.Resource) string { return r.RawName }
	envOf := func(r harvest.Resource) string {
		if r.Address.Type == "aws_vpc" {
			return sharedEnv
		}
		return "dev"
	}
	dirOf := func(_ string, r harvest.Resource) string {
		return filepath.Join(out, "envs", envOf(r), "aws", account(r), "us-east-1")
	}
	unsplit := func(_ string, r harvest.Resource) string {
		return filepath.Join(out, "aws", account(r), "us-east-1")
	}
	kept := newKeptRoots(out)
	dirs := kept.dirs(dirOf, unsplit, envOf)
	for _, tc := range []struct {
		r    harvest.Resource
		want string
	}{
		{harvest.RecordBare("q-1", "111111111111", "aws_sqs_queue", "aws"), old},
		{harvest.RecordBare("vpc-1", "111111111111", "aws_vpc", "aws"), old},
		// Nothing generated before.
		{harvest.RecordBare("q-2", "222222222222", "aws_sqs_queue", "aws"), filepath.Join(out, "envs", "dev", "aws", "222222222222", "us-east-1")},
		// The environment's root was generated: it stays too.
		{harvest.RecordBare("q-3", "333333333333", "aws_sqs_queue", "aws"), split},
	} {
		if got := dirs("sqs", tc.r); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.r.Observed.ID, got, tc.want)
		}
	}
	want := []report.KeptRoot{{Path: "aws/111111111111/us-east-1", Environments: []string{"dev", sharedEnv}, Scope: "aws/111111111111/us-east-1"}}
	if got := kept.report("aws/111111111111/us-east-1"); !reflect.DeepEqual(got, want) {
		t.Errorf("kept: got %+v, want %+v", got, want)
	}
}

// With --regenerate, a root an earlier import generated hands its
// resources over to the roots of their environments where removed blocks
// can take them all: it isn't kept. One of whose resources the run lists
// only some, or whose addresses are only inferred, keeps them. Without
// --regenerate, every root keeps them but one with none left, as one an
// earlier run handed everything over from.
func TestKeptRootsSplit(t *testing.T) {
	out := t.TempDir()
	rootOf := func(account string) string { return filepath.Join(out, "aws", account, "us-east-1") }
	queue := func(id, name string) engine.Managed {
		return engine.Managed{Type: "aws_sqs_queue", ID: id, Address: "aws_sqs_queue." + name}
	}
	inferred := queue("q-5", "five")
	inferred.Unverified = true
	for account, had := range map[string][]engine.Managed{
		"111111111111": {queue("q-1", "one"), queue("q-2", "two")},
		"222222222222": {queue("q-3", "three"), queue("q-4", "four")},
		"333333333333": {inferred},
		"444444444444": nil,
	} {
		if err := writeCheckpoint(out, rootOf(account), checkpoint{Result: &engine.Result{Addresses: had}}); err != nil {
			t.Fatal(err)
		}
	}
	envOf := func(r harvest.Resource) string {
		if r.Observed.ID == "q-2" {
			return "prod"
		}
		return "dev"
	}
	dirOf := func(_ string, r harvest.Resource) string {
		return filepath.Join(out, "envs", envOf(r), "aws", r.RawName, "us-east-1")
	}
	unsplit := func(_ string, r harvest.Resource) string { return rootOf(r.RawName) }
	importID := func(r harvest.Resource) (string, bool) { return r.Observed.ID, true }
	resources := map[string][]harvest.Resource{"sqs": {
		harvest.RecordBare("q-1", "111111111111", "aws_sqs_queue", "aws"),
		harvest.RecordBare("q-2", "111111111111", "aws_sqs_queue", "aws"),
		harvest.RecordBare("q-3", "222222222222", "aws_sqs_queue", "aws"),
		harvest.RecordBare("q-5", "333333333333", "aws_sqs_queue", "aws"),
		harvest.RecordBare("q-6", "444444444444", "aws_sqs_queue", "aws"),
	}}
	for _, tc := range []struct {
		regenerate bool
		want       map[string]string // by import ID, the root
		kept       []string
	}{
		{true, map[string]string{
			"q-1": dirOf("", resources["sqs"][0]),
			"q-2": dirOf("", resources["sqs"][1]),
			"q-3": rootOf("222222222222"),
			"q-5": rootOf("333333333333"),
			"q-6": dirOf("", resources["sqs"][4]),
		}, []string{"aws/222222222222/us-east-1", "aws/333333333333/us-east-1"}},
		{false, map[string]string{
			"q-1": rootOf("111111111111"),
			"q-2": rootOf("111111111111"),
			"q-3": rootOf("222222222222"),
			"q-5": rootOf("333333333333"),
			"q-6": dirOf("", resources["sqs"][4]),
		}, []string{"aws/111111111111/us-east-1", "aws/222222222222/us-east-1", "aws/333333333333/us-east-1"}},
	} {
		kept := newKeptRoots(out)
		if err := kept.split(resources, dirOf, unsplit, importID, tc.regenerate); err != nil {
			t.Fatal(err)
		}
		dirs := kept.dirs(dirOf, unsplit, envOf)
		for _, r := range resources["sqs"] {
			if got := dirs("sqs", r); got != tc.want[r.Observed.ID] {
				t.Errorf("regenerate %v: %s goes to %s, want %s", tc.regenerate, r.Observed.ID, got, tc.want[r.Observed.ID])
			}
		}
		var paths []string
		for _, k := range kept.report("aws") {
			paths = append(paths, k.Path)
		}
		if !reflect.DeepEqual(paths, tc.kept) {
			t.Errorf("regenerate %v: kept %v, want %v", tc.regenerate, paths, tc.kept)
		}
	}
}

// A provider that can't say which account it lists in can't be in an
// environment that names accounts: the run warns of it, once, in the log
// and the report, rather than lay its roots out as if there were none.
func TestWarnUnscoped(t *testing.T) {
	run := NewRun()
	run.environments = config.Environments{
		"prod":   {AWS: &config.AWSScopes{Accounts: []string{"111111111111"}}},
		"shared": {Tags: map[string]string{"Environment": "shared"}},
	}
	aws := &namedProvider{name: "aws"}

	run.warnUnscoped(aws, "aws/111111111111/eu-west-2")
	if len(run.report.Warnings) != 0 {
		t.Fatalf("with an account: warned %v", run.report.Warnings)
	}
	run.warnUnscoped(&namedProvider{name: "google"}, "")
	if len(run.report.Warnings) != 0 {
		t.Fatalf("no environment names a project: warned %v", run.report.Warnings)
	}
	run.warnUnscoped(aws, "")
	run.warnUnscoped(aws, "")
	if len(run.report.Warnings) != 1 || !strings.Contains(run.report.Warnings[0], "aws couldn't say which account") || !strings.Contains(run.report.Warnings[0], "(prod)") {
		t.Fatalf("without an account: warned %v, want one warning naming prod", run.report.Warnings)
	}
	if md := run.report.Markdown(); !strings.Contains(md, "## Warnings") || !strings.Contains(md, run.report.Warnings[0]) {
		t.Errorf("report.md doesn't warn:\n%s", md)
	}
}

// --default-env goes with --env-tags, not with the configuration file's
// environments, which place resources by tags of their own.
func TestCheckEnvironmentFlagsDefaultEnv(t *testing.T) {
	envs := &config.File{Environments: config.Environments{"dev": {Tags: map[string]string{"Environment": "dev"}}}}
	for _, tc := range []struct {
		file       *config.File
		defaultEnv string
		wantErr    bool
	}{
		{envs, "", false},
		{envs, "dev", true},
		{&config.File{}, "dev", false},
	} {
		flags := pflag.NewFlagSet("import", pflag.ContinueOnError)
		flags.String("default-env", "", "")
		if err := flags.Set("default-env", tc.defaultEnv); err != nil {
			t.Fatal(err)
		}
		if err := checkEnvironmentFlags(tc.file, flags); (err != nil) != tc.wantErr {
			t.Errorf("--default-env=%q, %d environments: %v", tc.defaultEnv, len(tc.file.Environments), err)
		}
	}
}
