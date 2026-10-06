// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import "testing"

func TestMerge(t *testing.T) {
	var r Report
	first := Report{Manifest: Manifest{Tool: Component{Name: "infraharvest", Version: "1.0.0"}, Provider: Provider{Source: "hashicorp/aws"}}}
	first.Directories = []Directory{{Path: "aws/111111111111/eu-west-2", Scope: "aws/111111111111/eu-west-2"}}
	first.AddDiscovered("aws/111111111111/eu-west-2", 3)
	second := Report{Manifest: Manifest{Tool: Component{Name: "infraharvest", Version: "1.0.0"}, Provider: Provider{Source: "hashicorp/aws", Version: "6.1.0"}}}
	second.Directories = []Directory{{Path: "aws/222222222222/eu-west-2", Scope: "aws/222222222222/eu-west-2"}}
	second.Excluded = []Excluded{{Type: "aws_vpc", ID: "vpc-1", Reason: "default VPC", Scope: "aws/222222222222/eu-west-2"}}
	second.Failures = []string{"aws/222222222222/eu-west-2: a check failed"}
	second.AddDiscovered("aws/222222222222/eu-west-2", 2)
	second.AddDiscovered("aws/111111111111/eu-west-2", 1)

	r.Merge(&first)
	r.Merge(&second)

	if r.Tool.Name != "infraharvest" || r.Provider.Version != "6.1.0" {
		t.Errorf("manifest %+v", r.Manifest)
	}
	if len(r.Directories) != 2 || len(r.Excluded) != 1 || len(r.Failures) != 1 {
		t.Errorf("coverage %+v", r.Coverage)
	}
	if len(r.Scopes) != 2 || r.Scopes[0].Discovered != 4 || r.Scopes[1].Discovered != 2 {
		t.Errorf("scopes %+v", r.Scopes)
	}
}
