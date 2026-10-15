// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
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
		if got := withEnvLayout(tc.pattern, tc.tags); got != tc.want {
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

	got := environments(resources, tags, defaultEnvTags, parents, importID)
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
}

// Each root's resources in other roots are external to it.
func TestWithOtherRoots(t *testing.T) {
	byDir := map[string][]engine.Import{
		"envs/prod":    {{Type: "aws_s3_bucket", ID: "logs-prod"}},
		"envs/staging": {{Type: "aws_s3_bucket", ID: "logs-staging"}, {Type: "aws_sqs_queue", ID: "https://sqs/jobs"}},
	}
	opts := engine.Options{External: []engine.External{{Type: "aws_vpc", ID: "vpc-1"}}}
	got := withOtherRoots(opts, byDir, "envs/prod").External
	want := []engine.External{{Type: "aws_s3_bucket", ID: "logs-staging"}, {Type: "aws_sqs_queue", ID: "https://sqs/jobs"}, {Type: "aws_vpc", ID: "vpc-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(opts.External) != 1 {
		t.Error("opts changed")
	}
	if one := withOtherRoots(opts, map[string][]engine.Import{"a": nil}, "a"); len(one.External) != 1 {
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
	got, _ := importsByDir("aws", options, map[string][]harvest.Resource{"s3": {prod}, "vpc": {shared}}, listerID, envOf, rawName, noFile)
	if len(got["out/envs/prod/aws"]) != 1 || len(got["out/envs/shared/aws"]) != 1 {
		t.Errorf("roots: %v", got)
	}
}

func rawName(r harvest.Resource) string      { return r.RawName }
func noFile(string, harvest.Resource) string { return "" }
