# Resources the end-to-end test of merging Google Cloud environments
# (environments_google_test.go) creates in floci-gcp: a bucket of dev and
# one of prod in one project, told apart by their env label, as the
# configuration file's environments tell them apart. The provider reaches
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

locals {
  environments = {
    dev = { token = "dev", class = "STANDARD" }
    prd = { token = "prd", class = "NEARLINE" }
  }
}

resource "google_storage_bucket" "orders" {
  for_each = local.environments

  name                        = "infraharvest-e2e-${each.value.token}-orders"
  location                    = "US"
  storage_class               = each.value.class
  uniform_bucket_level_access = true
  force_destroy               = true
  labels                      = { env = each.value.token, team = "shop" }
  public_access_prevention    = "inherited"

  versioning {
    enabled = true
  }

  soft_delete_policy {
    retention_duration_seconds = 604800
  }
}
