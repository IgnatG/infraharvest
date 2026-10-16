# Resources the Google Cloud end-to-end test creates in floci-gcp before importing
# them. The provider reaches the emulator through the GOOGLE_*_CUSTOM_ENDPOINT
# variables the test sets, like infraharvest's output.

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }
}

provider "google" {
  project = "infraharvest-e2e"
  region  = "us-central1"

  # Without the goog-terraform-provisioned label, which tells infraharvest
  # another state owns a resource: the import is what is tested.
  add_terraform_attribution_label = false
}

locals {
  name = "infraharvest-e2e"
}

resource "google_compute_network" "main" {
  name                    = "${local.name}-main"
  auto_create_subnetworks = false

  # Google returns them for every network; floci-gcp only what was set.
  routing_mode                 = "REGIONAL"
  bgp_best_path_selection_mode = "LEGACY"
}

resource "google_compute_subnetwork" "app" {
  name          = "${local.name}-app"
  region        = "us-central1"
  network       = google_compute_network.main.id
  ip_cidr_range = "10.10.0.0/24"
}

resource "google_compute_firewall" "https" {
  name          = "${local.name}-https"
  network       = google_compute_network.main.name
  source_ranges = ["10.0.0.0/8"]

  allow {
    protocol = "tcp"
    ports    = ["443"]
  }
}

resource "google_storage_bucket" "assets" {
  name                        = "${local.name}-assets"
  location                    = "US"
  uniform_bucket_level_access = true
  force_destroy               = true
  labels                      = { team = "web", env = "prod" }
  # Set, as the bucket module always sets them: a bucket whose versioning
  # was never set plans a change in the module and stays in the root.
  public_access_prevention = "inherited"

  versioning {
    enabled = true
  }

  soft_delete_policy {
    retention_duration_seconds = 604800
  }
}

resource "google_pubsub_topic" "events" {
  name = "${local.name}-events"
}

resource "google_pubsub_subscription" "worker" {
  name                 = "${local.name}-worker"
  topic                = google_pubsub_topic.events.id
  ack_deadline_seconds = 30
}

# A service account with a role in the project: a call of the
# service-accounts module's simple-sa.
resource "google_service_account" "worker" {
  account_id   = "worker"
  display_name = "Pub/Sub worker"
}

resource "google_project_iam_member" "worker" {
  project = "infraharvest-e2e"
  role    = "roles/pubsub.subscriber"
  member  = google_service_account.worker.member
}

# Grants to people and a group, one on a condition: a call of the iam
# module's projects_iam.
resource "google_project_iam_member" "viewers" {
  for_each = toset(["user:alice@example.com", "group:ops@example.com"])
  project  = "infraharvest-e2e"
  role     = "roles/viewer"
  member   = each.key
}

resource "google_project_iam_member" "auditor" {
  project = "infraharvest-e2e"
  role    = "roles/iam.securityReviewer"
  member  = "user:alice@example.com"
  condition {
    title       = "until-2030"
    description = "Access for the audit"
    expression  = "request.time < timestamp(\"2030-01-01T00:00:00Z\")"
  }
}

# A key ring whose keys share their settings: a call of the kms module.
resource "google_kms_key_ring" "app" {
  name     = "${local.name}-app"
  location = "us-central1"
}

resource "google_kms_crypto_key" "app" {
  for_each        = toset(["data", "signing"])
  name            = each.key
  key_ring        = google_kms_key_ring.app.id
  rotation_period = "7776000s"
}
