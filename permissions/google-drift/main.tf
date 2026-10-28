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

locals {
  # The roots' state objects, <prefix>/<root path>/default.tfstate.
  state_prefix  = trim(var.state_prefix, "/")
  state_objects = local.state_prefix == "" ? "gs://${var.state_bucket}" : "gs://${var.state_bucket}/${local.state_prefix}/"
}

# Reads the roots' state objects (storage.objects.get) and finds their
# workspaces (storage.objects.list), on the state's bucket alone. Drift
# plans with -lock=false, so it never creates the lock object.
#
# With a state_prefix, a condition limits the reads to the objects under
# it. Listing is checked on the bucket, whose name no object prefix
# matches, so the condition allows the bucket's checks too: the member
# lists every object's name in the bucket, as s3:ListBucket does in
# permissions/aws-drift, but reads only the state's. Conditions take
# uniform bucket-level access on the bucket.
resource "google_storage_bucket_iam_member" "state" {
  bucket = var.state_bucket
  role   = "roles/storage.objectViewer"
  member = var.member

  dynamic "condition" {
    for_each = local.state_prefix == "" ? [] : [local.state_prefix]

    content {
      title       = "infraharvest-drift-state"
      description = "infraharvest drift: the roots' state under ${condition.value}/ alone."
      expression  = "resource.type == \"storage.googleapis.com/Bucket\" || resource.name.startsWith(\"projects/_/buckets/${var.state_bucket}/objects/${condition.value}/\")"
    }
  }
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

  # The data reads permissions/google denies, but for Cloud Storage
  # objects: a deny policy can't spare the state's bucket, as its
  # conditions match resource tags alone, so objects stay readable.
  denied_data_reads = [for permission in jsondecode(file("${path.module}/../google/deny-data-reads.json")) : permission if !startswith(permission, "storage.googleapis.com/")]
}

# roles/viewer also reads data, which drift never needs, so deny it
# explicitly. Managing deny policies takes the Deny Admin role on the
# organization, so a project outside one, or an applier without it, sets
# deny_data_reads to false.
resource "google_iam_deny_policy" "data_reads" {
  count = var.deny_data_reads ? 1 : 0

  parent       = urlencode("cloudresourcemanager.googleapis.com/projects/${var.project_id}")
  name         = "infraharvest-drift-deny-data-reads"
  display_name = "infraharvest drift: no data reads"

  rules {
    description = "Deny infraharvest drift's member the data reads roles/viewer allows, but for the state's objects."

    deny_rule {
      denied_principals  = [local.denied_principal]
      denied_permissions = local.denied_data_reads
    }
  }
}
