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

locals {
  # The member as a deny policy names it: deny policies take principal
  # identifiers, not the member strings allow policies take.
  denied_principal = (
    startswith(var.member, "serviceAccount:") ? "principal://iam.googleapis.com/projects/-/serviceAccounts/${trimprefix(var.member, "serviceAccount:")}" :
    startswith(var.member, "user:") ? "principal://goog/subject/${trimprefix(var.member, "user:")}" :
    startswith(var.member, "group:") ? "principalSet://goog/group/${trimprefix(var.member, "group:")}" :
    var.member
  )

  # Data reads roles/viewer allows, directly or through the project's
  # convenience values (projectReaders on BigQuery datasets, projectViewers
  # on fine-grained Cloud Storage buckets and objects): log entries, read
  # directly, through a log view or copied out of their bucket, table,
  # model, row and entity data, objects, downloads, function source, traces
  # and health records. Reading through a log view takes views.access,
  # which view-level grants give, so it is denied as well. infraharvest
  # lists through Cloud Asset Inventory and the services' configuration
  # APIs, and the generated roots import no data, so none of them needs
  # these. permissions/google-drift reads the same list.
  denied_data_reads = jsondecode(file("${path.module}/deny-data-reads.json"))
}

# roles/viewer also reads data, which infraharvest never needs, so deny it
# explicitly, as permissions/aws does. Managing deny policies takes the
# Deny Admin role on the organization, so a project outside one, or an
# applier without it, sets deny_data_reads to false.
resource "google_iam_deny_policy" "data_reads" {
  count = var.deny_data_reads ? 1 : 0

  parent       = urlencode("cloudresourcemanager.googleapis.com/projects/${var.project_id}")
  name         = "infraharvest-deny-data-reads"
  display_name = "infraharvest: no data reads"

  rules {
    description = "Deny infraharvest's member the data reads roles/viewer allows."

    deny_rule {
      denied_principals  = [local.denied_principal]
      denied_permissions = local.denied_data_reads
    }
  }
}
