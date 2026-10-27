# The drift member's deny policy on data reads: it denies what
# permissions/google denies but the objects the state is, and can be left
# out. Planned against a mock provider, so it needs no Google Cloud
# project.

mock_provider "google" {}

variables {
  project_id   = "acme-prod"
  member       = "serviceAccount:drift@acme-prod.iam.gserviceaccount.com"
  state_bucket = "acme-tfstate"
}

run "data_reads_denied_but_objects" {
  command = plan

  assert {
    condition     = google_iam_deny_policy.data_reads[0].rules[0].deny_rule[0].denied_principals == tolist(["principal://iam.googleapis.com/projects/-/serviceAccounts/drift@acme-prod.iam.gserviceaccount.com"])
    error_message = "The deny policy doesn't name the service account as a principal identifier."
  }

  assert {
    condition = length(setsubtract([
      "logging.googleapis.com/logEntries.list",
      "bigquery.googleapis.com/tables.getData",
      "datastore.googleapis.com/entities.get",
    ], google_iam_deny_policy.data_reads[0].rules[0].deny_rule[0].denied_permissions)) == 0
    error_message = "A data read roles/viewer allows isn't denied."
  }

  assert {
    condition     = length([for p in google_iam_deny_policy.data_reads[0].rules[0].deny_rule[0].denied_permissions : p if startswith(p, "storage.googleapis.com/")]) == 0
    error_message = "Cloud Storage reads are denied, which drift needs for the state."
  }
}

run "deny_policy_left_out" {
  command = plan

  variables {
    deny_data_reads = false
  }

  assert {
    condition     = length(google_iam_deny_policy.data_reads) == 0
    error_message = "The deny policy is attached with deny_data_reads=false."
  }
}
