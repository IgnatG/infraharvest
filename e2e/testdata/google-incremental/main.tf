# A resource the Google Cloud incremental test adds to what testdata/google
# creates in floci-gcp once the first import is done, for --incremental to
# add to the root.

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

resource "google_pubsub_topic" "added" {
  name = "infraharvest-e2e-added"
}
