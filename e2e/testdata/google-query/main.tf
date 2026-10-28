# What the query service's end-to-end test creates in floci-gcp: a service
# enabled in the project (google_project_service), a type no lister
# lists, which the query service lists through Terraform's list
# resources. floci-gcp serves no network firewall policies, which the
# sandbox test lists instead. The provider reaches the emulator through
# the GOOGLE_*_CUSTOM_ENDPOINT variables the test sets.

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

resource "google_project_service" "pubsub" {
  service            = "pubsub.googleapis.com"
  disable_on_destroy = false
}
