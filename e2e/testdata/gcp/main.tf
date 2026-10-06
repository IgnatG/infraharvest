# Resources the GCP end-to-end test creates in floci-gcp before importing
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
}

locals {
  name = "infraharvest-e2e"
}

resource "google_compute_network" "main" {
  name                    = "${local.name}-main"
  auto_create_subnetworks = false
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
  labels                      = { team = "web" }
}

resource "google_pubsub_topic" "events" {
  name = "${local.name}-events"
}

resource "google_pubsub_subscription" "worker" {
  name                 = "${local.name}-worker"
  topic                = google_pubsub_topic.events.id
  ack_deadline_seconds = 30
}
