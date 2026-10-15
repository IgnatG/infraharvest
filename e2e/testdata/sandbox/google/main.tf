# Resources the Google Cloud sandbox test (sandbox_google_test.go) creates
# in a real project before importing them: what floci-gcp can't cover,
# such as real Cloud Asset Inventory search results, Cloud DNS zones and
# record sets, and a Cloud Router, and whether the read-only template
# (permissions/google) grants enough. The project and its APIs come from
# the sandbox bootstrap (testdata/sandbox/google-bootstrap), which lets
# the fixture service account manage these kinds of resources: nothing
# bills by the hour, and it is destroyed after each run.
# The provider signs in with the credentials file the test names in
# GOOGLE_APPLICATION_CREDENTIALS: the fixture service account's.

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same project, such as one per engine."
  type        = string
  default     = "terraform"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{0,8}$", var.suffix))
    error_message = "Use up to nine lower-case letters and digits, starting with a letter: the suffix goes into the service account's ID, which is at most 30 characters."
  }
}

variable "project" {
  description = "The sandbox project, which the bootstrap prepares."
  type        = string
}

variable "region" {
  description = "Where the regional resources are: the subnetworks, the router and the bucket."
  type        = string
  default     = "europe-west2"
}

provider "google" {
  project = var.project
  region  = var.region
}

# Names no other run has used, as bucket names must be unique across
# Google Cloud.
resource "random_string" "id" {
  length  = 6
  upper   = false
  special = false
}

locals {
  # Everything a run creates is named after the prefix, and everything
  # that takes labels carries them, so a sweep finds what a run left.
  # Networks, subnetworks, firewall rules, routers and service accounts
  # take no labels: the sweep finds them by name.
  name   = "ih-sbx-${var.suffix}-${random_string.id.result}"
  labels = { project = "infraharvest-sandbox", infraharvest-sandbox = var.suffix }
}

output "name" {
  description = "What every resource's name starts with: the test waits for Cloud Asset Inventory to find them."
  value       = local.name
}

resource "google_compute_network" "main" {
  name                    = local.name
  description             = "infraharvest sandbox"
  auto_create_subnetworks = false
  routing_mode            = "REGIONAL"
}

resource "google_compute_subnetwork" "app" {
  name                     = "${local.name}-app"
  region                   = var.region
  network                  = google_compute_network.main.id
  ip_cidr_range            = "10.80.1.0/24"
  private_ip_google_access = true
}

resource "google_compute_subnetwork" "data" {
  name          = "${local.name}-data"
  region        = var.region
  network       = google_compute_network.main.id
  ip_cidr_range = "10.80.2.0/24"
}

resource "google_compute_firewall" "https" {
  name          = "${local.name}-https"
  network       = google_compute_network.main.id
  direction     = "INGRESS"
  source_ranges = ["10.80.0.0/16"]
  target_tags   = ["app"]

  allow {
    protocol = "tcp"
    ports    = ["443"]
  }
}

# A Cloud Router without NAT or BGP peers, which costs nothing.
resource "google_compute_router" "main" {
  name    = local.name
  region  = var.region
  network = google_compute_network.main.id

  bgp {
    asn = 64514
  }
}

# An empty bucket costs nothing.
resource "google_storage_bucket" "data" {
  name                        = local.name
  location                    = upper(var.region)
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = true
  labels                      = local.labels
}

# A topic and a subscription with no messages cost nothing.
resource "google_pubsub_topic" "events" {
  name   = local.name
  labels = local.labels
}

resource "google_pubsub_subscription" "events" {
  name                 = local.name
  topic                = google_pubsub_topic.events.id
  ack_deadline_seconds = 20
  labels               = local.labels
}

# A private zone, visible to the network, with a record set: a fraction of
# a cent for the minutes it exists, and no queries.
resource "google_dns_managed_zone" "internal" {
  name        = local.name
  dns_name    = "${local.name}.internal."
  description = "infraharvest sandbox"
  visibility  = "private"
  labels      = local.labels

  private_visibility_config {
    networks {
      network_url = google_compute_network.main.id
    }
  }
}

resource "google_dns_record_set" "app" {
  name         = "app.${google_dns_managed_zone.internal.dns_name}"
  managed_zone = google_dns_managed_zone.internal.name
  type         = "A"
  ttl          = 300
  rrdatas      = ["10.80.1.10"]
}

resource "google_service_account" "app" {
  account_id   = local.name
  display_name = local.name
  description  = "infraharvest sandbox: the import must find this service account."
}
