# google_compute_route, from data/google/compute.yaml

import {
  to = google_compute_route.to_onprem
  id = "projects/acme-prod/global/routes/to-onprem"
}

# projects/acme-prod/global/routes/default-route-0a1b2c3d4e5f6a7b: left out by default, would import as "projects/acme-prod/global/routes/default-route-0a1b2c3d4e5f6a7b": Google Cloud creates it with a network (to the internet) or a subnetwork range, and deletes it with them
# projects/acme-prod/global/routes/gke-prod-1a2b3c4d-0e1f2a3b: left out by default, would import as "projects/acme-prod/global/routes/gke-prod-1a2b3c4d-0e1f2a3b": GKE creates it for a cluster's nodes and deletes it with them
