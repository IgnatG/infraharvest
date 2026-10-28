// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// A plan error about a data source is the resources' that read it, so a
// step undoes only them.
func TestDataReaders(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), rootfile.GeneratedFileName, `resource "aws_ebs_volume" "data" {
  kms_key_id = data.aws_kms_alias.ebs.target_key_arn
}

resource "aws_ebs_volume" "logs" {
  size = 8
}
`)
	if got := dataReaders(f, "data.aws_kms_alias.ebs"); len(got) != 1 || got[0] != "aws_ebs_volume.data" {
		t.Errorf("got %v, want aws_ebs_volume.data", got)
	}
	for _, address := range []string{"", "aws_ebs_volume.logs", "data.aws_kms_alias.other"} {
		if got := dataReaders(f, address); len(got) != 0 {
			t.Errorf("%q: got %v, want none", address, got)
		}
	}
}

// postProcess plans as few times as it can and still checks every step
// against the plan: here once for the imported values, once to leave out
// a null, whose plan is then the one the steps compare with, and once for
// the tag and literal lifts together. Every plan with import blocks
// reads every imported resource again, so the count is what a root costs.
func TestPostProcessPlanCount(t *testing.T) {
	t.Parallel()
	dir, tf := liftTagsDir(t)
	writeConfig(t, dir, rootfile.GeneratedFileName, strings.Replace(networkGenerated, "  vpc_id = \"vpc-0def5678\"\n", "  propagating_vgws = null\n  vpc_id           = \"vpc-0def5678\"\n", 1))

	if _, err := postProcess(context.Background(), tf, dir, generating(Options{DefaultTags: &awsDefaultTags}), nil, nil); err != nil {
		t.Fatal(err)
	}

	if got, want := strings.Join(tf.calls, ","), "plan,show,schema,plan,show,schema,plan,show"; got != want {
		t.Errorf("calls: got %s, want %s", got, want)
	}
	generated := readFile(t, dir, rootfile.GeneratedFileName)
	if strings.Contains(generated, "propagating_vgws") || !strings.Contains(generated, "local.vpc_id") {
		t.Errorf("want the null left out and the literals lifted:\n%s", generated)
	}
	if !strings.Contains(readFile(t, dir, rootfile.ProvidersFileName), "default_tags") {
		t.Error("tags not lifted")
	}
}
