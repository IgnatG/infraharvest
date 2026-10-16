# google_compute_router, from data/google/compute.yaml

import {
  to = google_compute_router.edge
  id = "projects/acme-prod/regions/europe-west2/routers/edge"
}

# Left out where the plan stays the same, as the default: bgp.advertise_mode=DEFAULT, bgp.keepalive_interval=20, encrypted_interconnect_router=false
