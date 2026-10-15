# google_compute_network, from data/google/compute.yaml

import {
  to = google_compute_network.main
  id = "projects/acme-prod/global/networks/main"
}

# projects/acme-prod/global/networks/default: left out by default, would import as "projects/acme-prod/global/networks/default": the default network GCP creates in each project; select it to import it

# Kept in state only, so a plan may set: delete_bgp_always_compare_med
