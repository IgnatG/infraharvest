# The member's read access to the state: the whole bucket, or with a
# state_prefix the objects under it, while it still lists the bucket.
# Planned against a mock provider, so it needs no Google Cloud project.

mock_provider "google" {}

variables {
  project_id   = "acme-prod"
  member       = "serviceAccount:drift@acme-prod.iam.gserviceaccount.com"
  state_bucket = "acme-tfstate"
}

run "whole_bucket_without_prefix" {
  command = plan

  assert {
    condition     = length(google_storage_bucket_iam_member.state.condition) == 0
    error_message = "The state's bucket is read through a condition without a state_prefix."
  }

  assert {
    condition     = output.granted_roles[length(output.granted_roles) - 1] == "roles/storage.objectViewer on gs://acme-tfstate"
    error_message = "The output doesn't name the whole bucket."
  }
}

run "objects_under_prefix" {
  command = plan

  variables {
    state_prefix = "/imported/"
  }

  assert {
    condition     = google_storage_bucket_iam_member.state.condition[0].expression == "resource.type == \"storage.googleapis.com/Bucket\" || resource.name.startsWith(\"projects/_/buckets/acme-tfstate/objects/imported/\")"
    error_message = "The condition doesn't limit object reads to the prefix, or doesn't allow listing the bucket."
  }

  assert {
    condition     = output.granted_roles[length(output.granted_roles) - 1] == "roles/storage.objectViewer on gs://acme-tfstate/imported/"
    error_message = "The output doesn't name the prefix."
  }
}

run "quote_in_prefix_refused" {
  command = plan

  variables {
    state_prefix = "imported\") || true || (\""
  }

  expect_failures = [var.state_prefix]
}

run "bucket_name_refused" {
  command = plan

  variables {
    state_bucket = "acme-tfstate\") || true"
  }

  expect_failures = [var.state_bucket]
}
