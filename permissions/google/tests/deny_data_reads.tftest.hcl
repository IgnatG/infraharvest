# The deny policy on data reads: it names the member as a deny policy
# does, denies the data reads roles/viewer allows and none of the reads
# listing and importing need, and can be left out. Planned against a mock
# provider, so it needs no Google Cloud project.

mock_provider "google" {}

variables {
  project_id = "acme-prod"
  member     = "serviceAccount:infraharvest@acme-prod.iam.gserviceaccount.com"
}

run "data_reads_denied" {
  command = plan

  assert {
    condition     = google_iam_deny_policy.data_reads[0].parent == "cloudresourcemanager.googleapis.com%2Fprojects%2Facme-prod"
    error_message = "The deny policy isn't attached to the project."
  }

  assert {
    condition     = google_iam_deny_policy.data_reads[0].rules[0].deny_rule[0].denied_principals == tolist(["principal://iam.googleapis.com/projects/-/serviceAccounts/infraharvest@acme-prod.iam.gserviceaccount.com"])
    error_message = "The deny policy doesn't name the service account as a principal identifier."
  }

  assert {
    condition = length(setsubtract([
      "logging.googleapis.com/logEntries.list",
      "bigquery.googleapis.com/tables.getData",
      "storage.googleapis.com/objects.get",
      "storage.googleapis.com/objects.list",
      "spanner.googleapis.com/databases.select",
      "bigtable.googleapis.com/tables.readRows",
      "datastore.googleapis.com/entities.get",
    ], google_iam_deny_policy.data_reads[0].rules[0].deny_rule[0].denied_permissions)) == 0
    error_message = "A data read roles/viewer allows isn't denied."
  }

  assert {
    condition = length(setintersection([
      "cloudasset.googleapis.com/assets.searchAllResources",
      "cloudasset.googleapis.com/assets.searchAllIamPolicies",
      "storage.googleapis.com/buckets.get",
      "storage.googleapis.com/buckets.getIamPolicy",
      "bigquery.googleapis.com/tables.get",
      "logging.googleapis.com/logMetrics.get",
      "cloudfunctions.googleapis.com/functions.get",
    ], google_iam_deny_policy.data_reads[0].rules[0].deny_rule[0].denied_permissions)) == 0
    error_message = "A read that listing or importing needs is denied."
  }

  assert {
    condition     = toset(output.granted_roles) == toset(["roles/viewer", "roles/iam.securityReviewer", "roles/cloudasset.viewer"])
    error_message = "The read-only roles aren't granted."
  }
}

run "user_denied_as_subject" {
  command = plan

  variables {
    member = "user:alex@example.com"
  }

  assert {
    condition     = local.denied_principal == "principal://goog/subject/alex@example.com"
    error_message = "A user isn't named as principal://goog/subject/<email>."
  }
}

run "group_denied_as_principal_set" {
  command = plan

  variables {
    member = "group:platform@example.com"
  }

  assert {
    condition     = local.denied_principal == "principalSet://goog/group/platform@example.com"
    error_message = "A group isn't named as principalSet://goog/group/<email>."
  }
}

run "workload_identity_denied_as_given" {
  command = plan

  variables {
    member = "principalSet://iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/github/attribute.repository/acme/infra"
  }

  assert {
    condition     = local.denied_principal == "principalSet://iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/github/attribute.repository/acme/infra"
    error_message = "A workload identity principal set isn't named as given."
  }
}

run "deny_policy_left_out" {
  command = plan

  variables {
    deny_data_reads = false
  }

  assert {
    condition     = length(google_iam_deny_policy.data_reads) == 0 && length(output.denied_permissions) == 0
    error_message = "The deny policy is attached with deny_data_reads=false."
  }

  assert {
    condition     = length(google_project_iam_member.readonly) == 3
    error_message = "The read-only roles aren't granted without the deny policy."
  }
}
