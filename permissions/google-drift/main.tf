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
  denied_data_reads = [
    "logging.googleapis.com/logEntries.list",
    "logging.googleapis.com/views.access",
    "logging.googleapis.com/buckets.copyLogEntries",
    "bigquery.googleapis.com/tables.getData",
    "bigquery.googleapis.com/models.getData",
    "bigquery.googleapis.com/objectRefs.read",
    "spanner.googleapis.com/databases.read",
    "spanner.googleapis.com/databases.select",
    "bigtable.googleapis.com/tables.readRows",
    "bigtable.googleapis.com/authorizedViews.readRows",
    "bigtable.googleapis.com/logicalViews.readRows",
    "bigtable.googleapis.com/materializedViews.readRows",
    "datastore.googleapis.com/entities.get",
    "datastore.googleapis.com/entities.list",
    "artifactregistry.googleapis.com/repositories.downloadArtifacts",
    "artifactregistry.googleapis.com/files.download",
    "cloudfunctions.googleapis.com/functions.sourceCodeGet",
    "cloudtrace.googleapis.com/traces.get",
    "cloudtrace.googleapis.com/traces.list",
    "healthcare.googleapis.com/fhirResources.get",
    "healthcare.googleapis.com/fhirStores.searchResources",
    "healthcare.googleapis.com/dicomStores.dicomWebRead",
    "healthcare.googleapis.com/hl7V2Messages.get",
    "healthcare.googleapis.com/hl7V2Messages.list",
  ]
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
