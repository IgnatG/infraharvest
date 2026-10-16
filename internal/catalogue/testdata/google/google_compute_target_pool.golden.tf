# google_compute_target_pool, from data/google/compute.yaml

import {
  to = google_compute_target_pool.web
  id = "projects/acme-prod/regions/us-central1/targetPools/web"
}

# projects/acme-prod/regions/us-central1/targetPools/a0123456789abcdef0123456789abcdef: left out by default, would import as "projects/acme-prod/regions/us-central1/targetPools/a0123456789abcdef0123456789abcdef": GKE creates it for a LoadBalancer Service and deletes it with the Service
