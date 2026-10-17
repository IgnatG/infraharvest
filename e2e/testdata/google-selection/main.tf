# A resource the Google Cloud selection test adds to what testdata/google
# creates in floci-gcp: a firewall rule named like the rules of a project's
# default network, which the default selection leaves out, for the test to
# include in the selection file.

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

resource "google_compute_firewall" "default_allow" {
  name          = "default-allow-e2e"
  network       = "infraharvest-e2e-main"
  source_ranges = ["10.0.0.0/8"]

  allow {
    protocol = "tcp"
    ports    = ["8443"]
  }
}
