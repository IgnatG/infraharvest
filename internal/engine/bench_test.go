// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// The benchmarks below run on a root of benchResources resources, as a
// large account's single root would hold, with benchMoved of them moved
// into module calls or matched by sensitive values.
const (
	benchResources = 6000
	benchMoved     = 2000
)

// benchGenerated returns a generated.tf of n resources: subnets that refer
// to a VPC, and security groups and parameters that refer to the subnets,
// with tags, so that every resource has a few attributes and a nested
// value.
func benchGenerated(n int) []byte {
	var b strings.Builder
	for i := range n {
		switch i % 3 {
		case 0:
			fmt.Fprintf(&b, "resource \"aws_vpc\" \"v_%d\" {\n  cidr_block = \"10.%d.0.0/16\"\n  tags = {\n    Name = \"vpc-%d\"\n    env  = \"prod\"\n  }\n}\n\n", i, i%256, i)
		case 1:
			fmt.Fprintf(&b, "resource \"aws_subnet\" \"s_%d\" {\n  vpc_id     = aws_vpc.v_%d.id\n  cidr_block = \"10.%d.1.0/24\"\n  tags = {\n    Name = \"subnet-%d\"\n  }\n}\n\n", i, i-1, i%256, i)
		default:
			fmt.Fprintf(&b, "resource \"aws_security_group\" \"g_%d\" {\n  name   = \"group-%d\"\n  vpc_id = aws_vpc.v_%d.id\n  ingress {\n    description = \"from ${aws_subnet.s_%d.arn}\"\n    cidr_blocks = [aws_subnet.s_%d.cidr_block]\n  }\n}\n\n", i, i, i-2, i-1, i-1)
		}
	}
	return []byte(b.String())
}

// benchVPCs returns the addresses of the VPCs of benchGenerated(n): what
// the benchmarks move into module calls.
func benchVPCs(n int) []string {
	var vpcs []string
	for i := 0; i < n; i += 3 {
		vpcs = append(vpcs, fmt.Sprintf("aws_vpc.v_%d", i))
	}
	return vpcs
}

// benchBodies parses src for each run of a benchmark, outside its timer,
// since the renames change the file.
func benchBodies(b *testing.B, src []byte) []*hclwrite.Block {
	b.StopTimer()
	defer b.StartTimer()
	f, diags := hclwrite.ParseConfig(src, GeneratedFileName, hcl.InitialPos)
	if diags.HasErrors() {
		b.Fatal(diags)
	}
	return f.Body().Blocks()
}

// benchReferenceRenames returns the renames writeCalls makes when the VPCs
// of benchGenerated(n) move into module calls.
func benchReferenceRenames(tb testing.TB, n int) []referenceRename {
	var renames []referenceRename
	for i, vpc := range benchVPCs(n) {
		typ, name, _ := strings.Cut(vpc, ".")
		for _, attr := range []string{"id", "arn", "cidr_block"} {
			replacement, err := outputTokens(fmt.Sprintf("vpc_%d", i), "vpc_"+attr)
			if err != nil {
				tb.Fatal(err)
			}
			renames = append(renames, referenceRename{[]string{typ, name, attr}, replacement})
		}
	}
	return renames
}

// benchPrefixRenames returns the renames rewriteRoot makes when the VPCs
// of benchGenerated(n) move into module calls.
func benchPrefixRenames(n int) []prefixRename {
	var renames []prefixRename
	for i, vpc := range benchVPCs(n) {
		typ, name, _ := strings.Cut(vpc, ".")
		for _, attr := range moduleOutputs {
			renames = append(renames, prefixRename{[]string{typ, name, attr}, []string{"module", fmt.Sprintf("vpc_%d", i), "vpc_" + attr}})
		}
	}
	return renames
}

func BenchmarkReplaceAllReferences(b *testing.B) {
	src := benchGenerated(benchResources)
	renames := benchReferenceRenames(b, benchResources)
	b.ReportAllocs()
	for b.Loop() {
		byStart := indexRenames(len(renames), func(i int) []string { return renames[i].search })
		for _, block := range benchBodies(b, src) {
			replaceAllReferences(block.Body(), renames, byStart)
		}
	}
}

func BenchmarkRenameAll(b *testing.B) {
	src := benchGenerated(benchResources)
	renames := benchPrefixRenames(benchResources)
	b.ReportAllocs()
	for b.Loop() {
		byStart := indexRenames(len(renames), func(i int) []string { return renames[i].search })
		for _, block := range benchBodies(b, src) {
			renameAll(block.Body(), renames, byStart)
		}
	}
}
