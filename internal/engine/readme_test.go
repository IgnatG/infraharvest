// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"
)

func TestReadmeFile(t *testing.T) {
	t.Parallel()
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
		"1. Set the secret variables in `variables.tf`. infraharvest never writes secret values into the configuration: set each from your secret store, such as AWS Secrets Manager, Azure Key Vault or Google Secret Manager, or with `TF_VAR_<name>`, and keep the values out of version control (`.gitignore` excludes `.tfvars` files):\n\n" +
		"   - `aws_ssm_parameter_token_value`: value of `aws_ssm_parameter.token`\n\n" +
		"2. Run `terraform init` and `terraform plan`. Every resource should be planned for import, with no changes.\n" +
		"3. Run `terraform apply` to record the resources in state. It changes nothing in the cloud.\n" +
		"4. Delete `imports.tf`: the import blocks have done their job.\n" +
		"\n## Left out\n\n" +
		"Terraform couldn't import these resources, or generate valid configuration for them. `rejected.hcl` has their blocks and errors: fix them and move the import blocks into `imports.tf` and the resources into the configuration, or leave the resources out.\n\n" +
		"- `aws_sqs_queue.gone`\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// When the plan check passed with state-only updates, such as Google Cloud
// labels, the README says to expect them, rather than a plan with no
// changes.
func TestReadmeFileExpectsStateOnlyUpdates(t *testing.T) {
	t.Parallel()
	gate := Gate{{Name: CheckPlan, Passed: true, Details: []string{"google_pubsub_topic.orders: " + stateOnlyChange + " (labels, terraform_labels)"}}}
	got := string(readmeFile(&Result{Imported: []Import{{Type: "google_pubsub_topic", Name: "orders", ID: "projects/acme/topics/orders"}}, Gate: gate}))
	if !strings.Contains(got, "with no changes but the updates the checks below list") || strings.Contains(got, "with no changes.\n") {
		t.Errorf("got:\n%s", got)
	}
}

// What an incremental import found gone is listed by address and import
// ID, with what to do about it, and only when there is any.
func TestReadmeFileListsWhatIsNoLongerInTheCloud(t *testing.T) {
	t.Parallel()
	result := &Result{
		Imported:   []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "https://sqs.eu-west-2.amazonaws.com/123/jobs"}},
		NotInCloud: []Import{{Type: "aws_sqs_queue", Name: "orders", ID: "https://sqs.eu-west-2.amazonaws.com/123/orders"}},
	}

	got := string(readmeFile(result))

	for _, want := range []string{
		"\n## No longer in the cloud\n\n",
		"The next plan would create them again, and fails while `imports.tf` still has their import blocks.",
		"delete its resource block, and anything that refers to it, and plan; or, if it should exist, plan to create it again.\n\n",
		"- `aws_sqs_queue.orders` (`https://sqs.eu-west-2.amazonaws.com/123/orders`)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(string(readmeFile(&Result{Imported: result.Imported})), "No longer in the cloud") {
		t.Error("a root with nothing gone has the section")
	}
}
