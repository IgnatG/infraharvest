// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"strings"
	"testing"
)

func TestOwnedBy(t *testing.T) {
	for _, tc := range []struct {
		tags  map[string]string
		owner Owner
		owned bool
	}{
		{map[string]string{"ManagedBy": "Terraform"}, Owner{Tool: "Terraform", Tag: "ManagedBy=Terraform"}, true},
		{map[string]string{"managed-by": "terraform-cloud"}, Owner{Tool: "Terraform", Tag: "managed-by=terraform-cloud"}, true},
		{map[string]string{"managed_by": "OpenTofu"}, Owner{Tool: "OpenTofu", Tag: "managed_by=OpenTofu"}, true},
		{map[string]string{"MANAGEDBY": "pulumi"}, Owner{Tool: "Pulumi", Tag: "MANAGEDBY=pulumi"}, true},
		{map[string]string{"ManagedBy": "CloudFormation"}, Owner{Tool: "CloudFormation", Tag: "ManagedBy=CloudFormation"}, true},
		{map[string]string{"aws:cloudformation:stack-name": "app"}, Owner{Tool: "CloudFormation", Stack: "app", Tag: "aws:cloudformation:stack-name=app"}, true},
		{map[string]string{"goog-terraform-provisioned": "true"}, Owner{Tool: "Terraform", Tag: "goog-terraform-provisioned=true"}, true},
		{map[string]string{"Terraform": "true"}, Owner{Tool: "Terraform", Tag: "Terraform=true"}, true},
		// Not markers of an owner.
		{map[string]string{"ManagedBy": "platform-team"}, Owner{}, false},
		{map[string]string{"Terraform": "false"}, Owner{}, false},
		{map[string]string{"Name": "terraform-state"}, Owner{}, false},
		{nil, Owner{}, false},
	} {
		owner, owned := OwnedBy(tc.tags)
		if owner != tc.owner || owned != tc.owned {
			t.Errorf("OwnedBy(%v): got %+v, %v; want %+v, %v", tc.tags, owner, owned, tc.owner, tc.owned)
		}
	}
}

// Another state's resources were imported again, with two owners. Listing
// doesn't read every type's tags, such as IAM roles', so Generate looks at
// the tags Terraform generated too.
func TestGenerateLeavesOutOwnedResources(t *testing.T) {
	generated := `resource "aws_iam_role" "app" {
  name = "app"
}

resource "aws_iam_role" "other" {
  name = "other"
  tags = {
    ManagedBy = "Terraform"
  }
}
`
	imports := []Import{{Type: "aws_iam_role", Name: "app", ID: "app"}, {Type: "aws_iam_role", Name: "other", ID: "other"}}

	dir := t.TempDir()
	result, err := Generate(context.Background(), &fakeTerraform{dir: dir, generated: generated}, dir, imports, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Owned) != 1 || result.Owned[0].Address != "aws_iam_role.other" || result.Owned[0].ID != "other" || result.Owned[0].Owner.Tool != "Terraform" {
		t.Errorf("owned: got %+v", result.Owned)
	}
	if len(result.Imported) != 1 || result.Imported[0].Name != "app" || len(result.Rejected) != 0 {
		t.Errorf("want only app imported, nothing rejected: %+v", result)
	}
	for _, name := range []string{GeneratedFileName, ImportsFileName} {
		if got := readFile(t, dir, name); strings.Contains(got, "other") {
			t.Errorf("%s keeps the owned role:\n%s", name, got)
		}
	}

	dir = t.TempDir()
	result, err = Generate(context.Background(), &fakeTerraform{dir: dir, generated: generated}, dir, imports, Options{IncludeOwned: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Owned) != 0 || len(result.Imported) != 2 {
		t.Errorf("IncludeOwned: want both imported, got %+v", result)
	}
}
