provider "google" {
  project = var.project_id
}

locals {
  # viewer reads resource configuration; securityReviewer adds the IAM
  # policies attached to resources, which Terraform imports as well;
  # cloudasset.viewer searches the project through Cloud Asset Inventory,
  # which infraharvest lists most resources with. The search also needs
  # the Cloud Asset API enabled in the project the caller's quota goes to.
  roles = toset(["roles/viewer", "roles/iam.securityReviewer", "roles/cloudasset.viewer"])
}

resource "google_project_iam_member" "readonly" {
  # checkov:skip=CKV_GCP_117:roles/viewer is the read-only access infraharvest needs to read resource configuration, granted on purpose
  for_each = local.roles

  project = var.project_id
  role    = each.value
  member  = var.member
}
