# google_compute_instance_template, from data/google/compute.yaml

import {
  to = google_compute_instance_template.web
  id = "projects/acme-prod/global/instanceTemplates/web"
}

# projects/acme-prod/global/instanceTemplates/gke-prod-default-pool-1a2b3c4d: left out by default, would import as "projects/acme-prod/global/instanceTemplates/gke-prod-default-pool-1a2b3c4d": GKE creates it for a cluster's nodes and deletes it with them
