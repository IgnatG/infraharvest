# google_compute_region_instance_template, from data/google/compute.yaml

import {
  to = google_compute_region_instance_template.web
  id = "projects/acme-prod/regions/us-central1/instanceTemplates/web"
}

# projects/acme-prod/regions/us-central1/instanceTemplates/gke-prod-default-pool-2b3c4d5e: left out by default, would import as "projects/acme-prod/regions/us-central1/instanceTemplates/gke-prod-default-pool-2b3c4d5e": GKE creates it for a cluster's nodes and deletes it with them
