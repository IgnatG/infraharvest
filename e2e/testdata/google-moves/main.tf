# Resources the end-to-end tests of --regenerate's removed blocks for
# Google Cloud (moves_google_test.go) create in floci-gcp, each with its
# own prefix for the buckets' names. Only each test's stand-in for Cloud
# Asset Inventory finds them, so no other test imports them. Applied again
# with handed_env=prod, the first bucket gets another environment label,
# which puts it in another root; the others stay as they are.

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

variable "prefix" {
  type    = string
  default = "infraharvest-e2e-moves"
}

variable "handed_env" {
  type    = string
  default = "dev"
}

resource "google_storage_bucket" "handed" {
  name                        = "${var.prefix}-handed"
  location                    = "US"
  uniform_bucket_level_access = true
  force_destroy               = true
  labels                      = { env = var.handed_env }
}

# Buckets that stay as they are, so that the root keeps more than one and
# labels them by name, and keeps their addresses.
resource "google_storage_bucket" "kept" {
  name                        = "${var.prefix}-kept"
  location                    = "US"
  uniform_bucket_level_access = true
  force_destroy               = true
  labels                      = { env = "dev" }
}

resource "google_storage_bucket" "steady" {
  name                        = "${var.prefix}-steady"
  location                    = "US"
  uniform_bucket_level_access = true
  force_destroy               = true
  labels                      = { env = "dev" }
}
