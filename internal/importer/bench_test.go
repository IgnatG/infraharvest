// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// BenchmarkOtherRoots is what generate does for each root of a call before
// it runs the engine: the options with the other roots' resources, and
// their fingerprint, for a {service} layout of 60 roots of 500 resources
// each.
func BenchmarkOtherRoots(b *testing.B) {
	const roots, resources = 60, 500
	out := b.TempDir()
	types := []string{"aws_vpc", "aws_subnet", "aws_security_group", "aws_iam_role", "aws_iam_role_policy_attachment", "aws_s3_bucket", "aws_s3_bucket_versioning", "aws_ssm_parameter"}
	byDir := map[string][]engine.Import{}
	for d := range roots {
		dir := filepath.Join(out, fmt.Sprintf("service_%02d", d))
		for i := range resources {
			typ := types[i%len(types)]
			byDir[dir] = append(byDir[dir], engine.Import{Type: typ, Name: fmt.Sprintf("r_%d_%d", d, i), ID: fmt.Sprintf("%s-%02d-%04d", typ, d, i)})
		}
	}
	opts := engine.Options{DataSources: map[string]engine.DataSource{
		"aws_vpc":          {Type: "aws_vpc", Argument: "id"},
		"aws_subnet":       {Type: "aws_subnet", Argument: "id"},
		"aws_iam_role":     {Type: "aws_iam_role", Argument: "name"},
		"aws_s3_bucket":    {Type: "aws_s3_bucket", Argument: "bucket"},
		"aws_kms_key":      {Type: "aws_kms_key", Argument: "key_id"},
		"aws_route53_zone": {Type: "aws_route53_zone", Argument: "zone_id"},
	}}
	b.ReportAllocs()
	for b.Loop() {
		for dir, imports := range byDir {
			dirOpts := withOtherRoots(opts, byDir, dir, out)
			if _, err := fingerprint("v1", imports, dirOpts); err != nil {
				b.Fatal(err)
			}
		}
	}
}
