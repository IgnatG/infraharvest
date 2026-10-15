# google_compute_instance_group_manager, from data/google/compute.yaml

import {
  to = google_compute_instance_group_manager.web
  id = "projects/acme-prod/zones/us-central1-a/instanceGroupManagers/web"
}

# projects/acme-prod/zones/us-central1-a/instanceGroupManagers/gke-prod-default-pool-1a2b3c4d-grp: left out by default, would import as "projects/acme-prod/zones/us-central1-a/instanceGroupManagers/gke-prod-default-pool-1a2b3c4d-grp": GKE creates it for a cluster's nodes and deletes it with them
