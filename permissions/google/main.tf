provider "google" {
  project = var.project_id
}

locals {
  # viewer reads resource configuration; securityReviewer adds the IAM
  # policies attached to resources, which Terraform imports as well.
  roles = toset(["roles/viewer", "roles/iam.securityReviewer"])
}

resource "google_project_iam_member" "readonly" {
  for_each = local.roles

  project = var.project_id
  role    = each.value
  member  = var.member
}
