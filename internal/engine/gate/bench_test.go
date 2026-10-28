// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The benchmark below runs on a root of benchResources resources, as a
// large account's single root would hold, with benchMoved sensitive
// values.
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

// benchValues returns n random values of the length of a password or a
// key, which the content never holds: the worst case, a scan that finds
// nothing and so reads every file to the end.
func benchValues(n int) []string {
	r := rand.New(rand.NewPCG(1, 2))
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	values := make([]string, n)
	for i := range values {
		v := make([]byte, 24+r.IntN(16))
		for j := range v {
			v[j] = letters[r.IntN(len(letters))]
		}
		values[i] = string(v)
	}
	return values
}

func BenchmarkScanSecrets(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "generated.tf"), benchGenerated(benchResources), 0o644); err != nil {
		b.Fatal(err)
	}
	sensitive := benchValues(benchMoved)
	b.ReportAllocs()
	for b.Loop() {
		check, err := scanSecrets(dir, sensitive)
		if err != nil || !check.Passed {
			b.Fatal(err, check.Details)
		}
	}
}
