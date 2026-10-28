// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// An edit that fails after changing a file leaves nothing of it behind.
func TestVerifyRestoresWhenTheEditFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	tf := &fakeTerraform{dir: dir}
	failure := errors.New("halfway")
	edit := func() (bool, error) {
		if err := os.WriteFile(filepath.Join(dir, rootfile.GeneratedFileName), []byte("# changed\n"), 0o644); err != nil {
			return false, err
		}
		if err := os.WriteFile(filepath.Join(dir, rootfile.LocalsFileName), []byte("locals {}\n"), 0o644); err != nil {
			return false, err
		}
		return true, failure
	}

	kept, err := verify(context.Background(), tf, dir, tfrun.ChangeSummary{}, nil, nil, edit, rootfile.GeneratedFileName, rootfile.LocalsFileName)
	if kept || !errors.Is(err, failure) {
		t.Fatalf("got kept=%v err=%v, want the edit's error", kept, err)
	}

	if got := readFile(t, dir, rootfile.GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, rootfile.LocalsFileName)); err == nil {
		t.Error("locals.tf not removed")
	}
	if len(tf.calls) != 0 {
		t.Errorf("planned after a failed edit: %v", tf.calls)
	}
}

// An edit is kept only if each resource plans the change it had, not only
// the same totals: one that hides another change in a resource that
// already updates, such as its labels after an import, is undone.
func TestVerifyComparesEachResourcesChange(t *testing.T) {
	t.Parallel()
	baseline := tfrun.ChangeSummary{Change: 1}
	updatesTags := resourceChange("aws_subnet.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"tags": map[string]any{}}, map[string]any{"tags": map[string]any{"a": "b"}})
	changes := map[string]string{"aws_subnet.a": tfrun.ChangeSignature(updatesTags)}
	edit := func(dir string) func() (bool, error) {
		return func() (bool, error) {
			return true, os.WriteFile(filepath.Join(dir, rootfile.GeneratedFileName), []byte("# edited\n"), 0o644)
		}
	}
	for name, tt := range map[string]struct {
		planned *tfjson.ResourceChange
		kept    bool
	}{
		"the same change": {updatesTags, true},
		"another change too": {resourceChange("aws_subnet.a", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"tags": map[string]any{}, "cidr_block": "10.0.1.0/24"},
			map[string]any{"tags": map[string]any{"a": "b"}, "cidr_block": "10.0.9.0/24"}), false},
	} {
		dir := t.TempDir()
		writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
		tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: baseline}}, showns: []*tfjson.Plan{{ResourceChanges: []*tfjson.ResourceChange{tt.planned}}}}

		kept, err := verify(context.Background(), tf, dir, baseline, changes, nil, edit(dir), rootfile.GeneratedFileName)
		if err != nil || kept != tt.kept {
			t.Errorf("%s: got kept=%v err=%v, want kept=%v", name, kept, err, tt.kept)
		}
		if restored := readFile(t, dir, rootfile.GeneratedFileName) == networkGenerated; restored == tt.kept {
			t.Errorf("%s: generated.tf restored=%v", name, restored)
		}
	}
}

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

// commentStep is a step that appends comment to generated.tf.
func commentStep(dir, comment string) tidyStep {
	return tidyStep{files: []string{rootfile.GeneratedFileName}, edit: func() (bool, error) {
		path := filepath.Join(dir, rootfile.GeneratedFileName)
		content, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		return true, os.WriteFile(path, append(content, []byte("# "+comment+"\n")...), 0o644)
	}}
}

// One plan checks steps that keep it as it was; only when it differs are
// they checked one at a time, so that the step the plan rejects is the
// only one undone.
func TestVerifySteps(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		plans []fakePlan
		calls string
		want  []string // the comments kept
	}{
		"together":          {nil, "plan,show", []string{"one", "two"}},
		"one at a time":     {[]fakePlan{{summary: tfrun.ChangeSummary{Change: 1}}, {summary: tfrun.ChangeSummary{Change: 1}}}, "plan,show,plan,show,plan,show", []string{"two"}},
		"none of them kept": {[]fakePlan{{summary: tfrun.ChangeSummary{Change: 1}}, {summary: tfrun.ChangeSummary{Change: 1}}, {summary: tfrun.ChangeSummary{Change: 1}}}, "plan,show,plan,show,plan,show", nil},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
			tf := &fakeTerraform{dir: dir, plans: tt.plans}

			if err := verifySteps(context.Background(), tf, dir, tfrun.ChangeSummary{}, nil, nil, []tidyStep{commentStep(dir, "one"), commentStep(dir, "two")}); err != nil {
				t.Fatal(err)
			}

			if got := strings.Join(tf.calls, ","); got != tt.calls {
				t.Errorf("calls: got %s, want %s", got, tt.calls)
			}
			want := networkGenerated
			for _, c := range tt.want {
				want += "# " + c + "\n"
			}
			if got := readFile(t, dir, rootfile.GeneratedFileName); got != want {
				t.Errorf("generated.tf: got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// Steps that change nothing need no plan.
func TestVerifyStepsWithoutChanges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	tf := &fakeTerraform{dir: dir}
	unchanged := tidyStep{files: []string{rootfile.GeneratedFileName}, edit: func() (bool, error) { return false, nil }}
	none := tidyStep{files: []string{rootfile.GeneratedFileName, rootfile.DataFileName}, each: func(map[string]int) (map[string]bool, error) { return nil, nil }}

	if err := verifySteps(context.Background(), tf, dir, tfrun.ChangeSummary{}, nil, nil, []tidyStep{unchanged, none}); err != nil {
		t.Fatal(err)
	}

	if len(tf.calls) != 0 {
		t.Errorf("calls: %v, want none", tf.calls)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf changed:\n%s", got)
	}
}

// postProcess runs its passes in this order, every one after the passes it
// names, each of which is one of postProcess's. A run pass plans on its
// own; a step pass's edits are verified with those of the steps next to it.
func TestTidyPassesRunAfterTheirPrerequisites(t *testing.T) {
	t.Parallel()
	passes := (&tidying{r: generating(Options{})}).passes()

	if err := checkPassOrder(passes); err != nil {
		t.Error(err)
	}
	want := []string{
		"run " + passEffectiveTags,
		"run " + passDefaults,
		"step " + passReferences,
		"step " + passDataSources,
		"step " + passRootVariables,
		"step " + passLookups,
		"step " + passTagLift,
		"step " + passLiterals,
		"run " + passCuratedModules,
		"step " + passOutputLists,
		"step " + passProjects,
		"step " + passInherited,
		"run " + passLocalModules,
	}
	var got []string
	for _, p := range passes {
		switch {
		case p.run != nil && p.step == nil:
			got = append(got, "run "+p.name)
		case p.run == nil && p.step != nil:
			got = append(got, "step "+p.name)
		default:
			t.Errorf("the %s pass needs either run or step", p.name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("passes:\ngot  %q\nwant %q", got, want)
	}
}

// A pass wired before one it must run after, or after one there isn't,
// fails the order.
func TestCheckPassOrderRejectsAPassBeforeItsPrerequisite(t *testing.T) {
	t.Parallel()
	tags := tidyPass{name: passTagLift}
	literals := tidyPass{name: passLiterals, after: []string{passTagLift}}

	if err := checkPassOrder([]tidyPass{tags, literals}); err != nil {
		t.Errorf("in order: %v", err)
	}
	if err := checkPassOrder([]tidyPass{literals, tags}); err == nil {
		t.Error("literals before the tag lift: want an error")
	}
	if err := checkPassOrder([]tidyPass{literals}); err == nil {
		t.Error("after a pass there isn't: want an error")
	}
}
