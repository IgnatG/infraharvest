# google_compute_subnetwork, from data/google/compute.yaml

import {
  to = google_compute_subnetwork.app
  id = "projects/acme-prod/regions/us-central1/subnetworks/app"
}

# projects/acme-prod/regions/us-central1/subnetworks/default: left out by default, would import as "projects/acme-prod/regions/us-central1/subnetworks/default": a subnetwork of the default network, which Google Cloud creates in each region; select it to import it
