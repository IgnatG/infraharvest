provider "google" {
  project = var.project_id
}

locals {
  # The roles permissions/google grants, which refreshing the roots needs:
  # left out when the member has them from there already.
  roles = var.grant_read_only ? toset(["roles/viewer", "roles/iam.securityReviewer", "roles/cloudasset.viewer"]) : toset([])
}

resource "google_project_iam_member" "readonly" {
  # checkov:skip=CKV_GCP_117:roles/viewer is the read-only access infraharvest needs to read resource configuration, granted on purpose
  for_each = local.roles

  project = var.project_id
  role    = each.value
  member  = var.member
}

# Applying drift's saved refresh plan to its private copy configures the
# provider again, which signs in again: nothing here grants that, but the
# member's credentials must still be valid then.

# Reads the roots' state objects (storage.objects.get) and finds their
# workspaces (storage.objects.list), on the state's bucket alone. Drift
# plans with -lock=false, so it never creates the lock object.
resource "google_storage_bucket_iam_member" "state" {
  bucket = var.state_bucket
  role   = "roles/storage.objectViewer"
  member = var.member
}
