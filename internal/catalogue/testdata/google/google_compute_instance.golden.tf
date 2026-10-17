# google_compute_instance, from data/google/compute.yaml

import {
  to = google_compute_instance.bastion
  id = "projects/acme-prod/zones/us-central1-a/instances/bastion"
}

# projects/acme-prod/zones/us-central1-a/instances/gke-prod-default-pool-1a2b3c4d-x9z8: left out by default, would import as "projects/acme-prod/zones/us-central1-a/instances/gke-prod-default-pool-1a2b3c4d-x9z8": GKE creates it for a cluster's nodes and deletes it with them
# projects/acme-prod/zones/us-central1-a/instances/web-x1b2: left out by default, would import as "projects/acme-prod/zones/us-central1-a/instances/web-x1b2": a managed instance group creates it, and deletes and recreates it as it scales and updates; select it to import it
