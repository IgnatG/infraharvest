# Resources the end-to-end test of merging Google Cloud environments
# (environments_google_test.go) creates in floci-gcp: a bucket of dev and
# one of prod in one project, told apart by their env label, as the
# configuration file's environments tell them apart, each with an owner
# label of its own. Applied again with events=true, dev gets a Pub/Sub
# topic, labelled as dev's unless events_env says otherwise; with
# reports=true, prod gets a reports bucket; with another dev_owner, dev's
# bucket gets another owner label. The provider reaches
# the emulator through the GOOGLE_*_CUSTOM_ENDPOINT variables the test
# sets.

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

  add_terraform_attribution_label = false
}

variable "dev_owner" {
  type    = string
  default = "alice"
}

variable "events" {
  type    = bool
  default = false
}

# The topic's env label; empty for none, which puts it in no environment.
variable "events_env" {
  type    = string
  default = "dev"
}

locals {
  environments = {
    dev = { token = "dev", class = "STANDARD", owner = var.dev_owner }
    prd = { token = "prd", class = "NEARLINE", owner = "bob" }
  }
}

resource "google_storage_bucket" "orders" {
  for_each = local.environments

  name                        = "infraharvest-e2e-${each.value.token}-orders"
  location                    = "US"
  storage_class               = each.value.class
  uniform_bucket_level_access = true
  force_destroy               = true
  labels                      = { env = each.value.token, team = "shop", owner = each.value.owner }
  public_access_prevention    = "inherited"

  versioning {
    enabled = true
  }

  soft_delete_policy {
    retention_duration_seconds = 604800
  }
}

resource "google_pubsub_topic" "events" {
  count = var.events ? 1 : 0

  name   = "infraharvest-e2e-dev-events"
  labels = var.events_env == "" ? {} : { env = var.events_env }
}

# A bucket of prod alone, which reports=true adds.
variable "reports" {
  type    = bool
  default = false
}

resource "google_storage_bucket" "reports" {
  count = var.reports ? 1 : 0

  name                        = "infraharvest-e2e-prd-reports"
  location                    = "US"
  storage_class               = "NEARLINE"
  uniform_bucket_level_access = true
  force_destroy               = true
  labels                      = { env = "prd", team = "shop", owner = "bob" }
  public_access_prevention    = "inherited"
}
