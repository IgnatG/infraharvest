// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"strings"
	"testing"
)

// splitScaleRoot is environment env's root of n queues, every other one
// with arguments that differ between the environments enough not to
// merge, and a policy per queue that refers to it, so that each split
// queue splits its policy too; a chain of policies in which only the
// last refers to a queue that splits; and a queue whose label a split
// part would take.
func splitScaleRoot(env string, n int) (string, map[string]string) {
	account := map[string]string{"dev": "111111111111", "prd": "222222222222"}[env]
	shift := map[string]int{"dev": 0, "prd": 1}[env]
	var b strings.Builder
	ids := map[string]string{}
	for i := range n {
		fmt.Fprintf(&b, "resource \"aws_sqs_queue\" \"q%d\" {\n  name = \"shop-%s-q%d\"\n", i, env, i)
		if i%2 == 1 {
			fmt.Fprintf(&b, "  delay_seconds              = %d\n  max_message_size           = %d\n  visibility_timeout_seconds = %d\n", 1+shift, 1024+shift, 30+shift)
		} else {
			b.WriteString("  delay_seconds              = 5\n  max_message_size           = 2048\n  visibility_timeout_seconds = 60\n")
		}
		b.WriteString("}\n\n")
		ids[fmt.Sprintf("aws_sqs_queue.q%d", i)] = fmt.Sprintf("https://sqs.eu-west-2.amazonaws.com/%s/shop-%s-q%d", account, env, i)
		fmt.Fprintf(&b, "resource \"aws_sqs_queue_policy\" \"p%d\" {\n  policy    = \"{}\"\n  queue_url = aws_sqs_queue.q%d.url\n}\n\n", i, i)
	}
	// c0 refers to c1, and so on, and the last to q1, which splits: the
	// whole chain splits, from its end.
	chain := min(n, 20)
	for i := range chain {
		to := fmt.Sprintf("aws_sqs_queue_policy.c%d.id", i+1)
		if i == chain-1 {
			to = "aws_sqs_queue.q1.url"
		}
		fmt.Fprintf(&b, "resource \"aws_sqs_queue_policy\" \"c%d\" {\n  policy    = \"{}\"\n  queue_url = %s\n}\n\n", i, to)
	}
	// q1 splits into q1_dev and q1_prod.
	fmt.Fprintf(&b, "resource \"aws_sqs_queue\" \"q1_prod\" {\n  name = \"shop-%s-q1-prod\"\n}\n\n", env)
	ids["aws_sqs_queue.q1_prod"] = fmt.Sprintf("https://sqs.eu-west-2.amazonaws.com/%s/shop-%s-q1-prod", account, env)
	return b.String(), ids
}

func splitScaleRoots(t testing.TB, n int) []envRoot {
	dev, devIDs := splitScaleRoot("dev", n)
	prd, prdIDs := splitScaleRoot("prd", n)
	return []envRoot{
		awsEnvRoot(t, "dev", nil, "111111111111", dev, devIDs, nil, nil, ""),
		awsEnvRoot(t, "prod", []string{"prd"}, "222222222222", prd, prdIDs, nil, nil, ""),
	}
}

// TestGoldenSplitScale merges an estate in which half the resources split,
// some only once others have, and a split part's label is taken, and
// compares the merged root with
// testdata/envroot/aws-split-scale.golden.txt: the labels the split parts
// get depend on the order they split in.
func TestGoldenSplitScale(t *testing.T) {
	m := mergeStack("aws", mergeProfiles["aws"], splitScaleRoots(t, 40), rootSchemas)
	files, err := mergedRootFiles(m)
	if err != nil {
		t.Fatal(err)
	}
	goldenRoot(t, "aws-split-scale", files)
}

// BenchmarkSplitResidue merges dev and prd of n queues each, half of
// which don't merge, with a policy per queue that refers to it.
func BenchmarkSplitResidue(b *testing.B) {
	for _, n := range []int{250, 500, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			roots := splitScaleRoots(b, n)
			for b.Loop() {
				mergeStack("aws", mergeProfiles["aws"], roots, rootSchemas)
			}
		})
	}
}
