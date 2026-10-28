// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"strings"
	"testing"
)

// forEachScaleRoot is environment env's root of a synthetic estate of n
// queues, n/2 functions and n policies, with the import IDs that match
// them across environments: queues of a few shapes, some with arguments
// no other queue has, some only dev has; functions whose nested blocks
// differ; and policies, some only dev has, that refer to queues forEach groups
// take first, so how a reference is written changes while the policies
// are grouped.
func forEachScaleRoot(env string, n int, uniqueShapes bool) (string, map[string]string) {
	account := map[string]string{"dev": "111111111111", "prd": "222222222222"}[env]
	var b strings.Builder
	ids := map[string]string{}
	for i := range n {
		if env != "dev" && i%7 == 3 {
			continue
		}
		fmt.Fprintf(&b, "resource \"aws_sqs_queue\" \"q%d\" {\n  delay_seconds              = %d\n  name                       = \"shop-%s-q%d\"\n  visibility_timeout_seconds = 30\n", i, i%3, env, i)
		if i%4 == 1 {
			fmt.Fprintf(&b, "  max_message_size           = %d\n", 1024+i)
		}
		if i%5 == 2 {
			fmt.Fprintf(&b, "  kms_master_key_id          = \"alias/k%d\"\n", i%2)
		}
		if uniqueShapes || i%6 == 5 {
			fmt.Fprintf(&b, "  tag_%d = \"x\"\n  tag_%db = \"y\"\n  tag_%dc = \"z\"\n", i, i, i)
		}
		b.WriteString("}\n\n")
		ids[fmt.Sprintf("aws_sqs_queue.q%d", i)] = fmt.Sprintf("https://sqs.eu-west-2.amazonaws.com/%s/shop-%s-q%d", account, env, i)
	}
	for i := range n / 2 {
		mode := "Active"
		if i%2 == 1 {
			mode = "PassThrough"
		}
		fmt.Fprintf(&b, "resource \"aws_lambda_function\" \"f%d\" {\n  function_name = \"shop-%s-f%d\"\n  handler       = \"index.handler\"\n  runtime       = \"nodejs%d.x\"\n\n  tracing_config {\n    mode = %q\n  }\n}\n\n", i, env, i, 20+i%3, mode)
		ids[fmt.Sprintf("aws_lambda_function.f%d", i)] = fmt.Sprintf("shop-%s-f%d", env, i)
	}
	for i := range n {
		if env != "dev" && i%7 == 3 {
			continue
		}
		fmt.Fprintf(&b, "resource \"aws_sqs_queue_policy\" \"p%d\" {\n  policy    = \"{}\"\n  queue_url = aws_sqs_queue.q%d.url\n}\n\n", i, i%3)
	}
	return b.String(), ids
}

// forEachScaleRoots are dev's and prd's roots of forEachScaleRoot.
func forEachScaleRoots(t testing.TB, n int, uniqueShapes bool) []envRoot {
	dev, devIDs := forEachScaleRoot("dev", n, uniqueShapes)
	prd, prdIDs := forEachScaleRoot("prd", n, uniqueShapes)
	return []envRoot{
		awsEnvRoot(t, "dev", nil, "111111111111", dev, devIDs, nil, nil, ""),
		awsEnvRoot(t, "prod", []string{"prd"}, "222222222222", prd, prdIDs, nil, nil, ""),
	}
}

// TestGoldenForEachScale merges a larger estate, with many resources of
// each type to group, and compares the merged root with
// testdata/envroot/aws-for-each-scale.golden.txt.
func TestGoldenForEachScale(t *testing.T) {
	m := mergeStack("aws", mergeProfiles["aws"], forEachScaleRoots(t, 60, false), rootSchemas)
	if len(m.forEach) == 0 {
		t.Fatal("no forEach: the estate should group")
	}
	files, err := mergedRootFiles(m)
	if err != nil {
		t.Fatal(err)
	}
	goldenRoot(t, "aws-for-each-scale", files)
}

// BenchmarkGroupForEach merges dev and prd of n queues each, every queue
// of its own shape, so no two group and every pair is compared.
func BenchmarkGroupForEach(b *testing.B) {
	for _, n := range []int{250, 500, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			roots := forEachScaleRoots(b, n, true)
			for b.Loop() {
				mergeStack("aws", mergeProfiles["aws"], roots, rootSchemas)
			}
		})
	}
}
