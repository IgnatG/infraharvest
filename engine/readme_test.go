// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "testing"

func TestReadmeFile(t *testing.T) {
	result := &Result{
		Imported: []Import{
			{Type: "aws_ssm_parameter", Name: "token", ID: "/app/token"},
			{Type: "aws_sqs_queue", Name: "jobs", ID: "q1"},
		},
		Secrets:  []Secret{{Variable: "aws_ssm_parameter_token_value", Address: "aws_ssm_parameter.token", Attribute: "value"}},
		Rejected: []Rejection{{Address: "aws_sqs_queue.gone", Errors: []string{"Cannot import non-existent remote object"}}},
	}

	got := string(readmeFile(result))

	want := "# Imported resources\n\n" +
		"infraharvest generated this configuration for 2 existing resources:\n\n" +
		"| Type | Count |\n|---|---|\n" +
		"| `aws_sqs_queue` | 1 |\n" +
		"| `aws_ssm_parameter` | 1 |\n" +
		"\n## Next steps\n\n" +
		"1. Set the secret variables in `variables.tf`. Terraform doesn't write secret values into the configuration it generates. Keep the values out of version control, for example in a `.tfvars` file (`.gitignore` excludes them):\n\n" +
		"   - `aws_ssm_parameter_token_value`: value of `aws_ssm_parameter.token`\n\n" +
		"2. Run `terraform init` and `terraform plan`. Every resource should be planned for import, with no changes.\n" +
		"3. Run `terraform apply` to record the resources in state. It changes nothing in the cloud.\n" +
		"4. Delete `imports.tf`: the import blocks have done their job.\n" +
		"\n## Left out\n\n" +
		"Terraform couldn't import these resources, or generate valid configuration for them. `rejected.hcl` has their blocks and errors: fix them and move the blocks into `imports.tf` and `generated.tf`, or leave the resources out.\n\n" +
		"- `aws_sqs_queue.gone`\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
