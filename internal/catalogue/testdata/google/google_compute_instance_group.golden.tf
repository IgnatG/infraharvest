# google_compute_instance_group, from data/google/compute.yaml

import {
  to = google_compute_instance_group.web
  id = "projects/acme-prod/zones/us-central1-a/instanceGroups/web"
}

# projects/acme-prod/zones/us-central1-a/instanceGroups/gke-prod-default-pool-1a2b3c4d-grp: left out by default, would import as "projects/acme-prod/zones/us-central1-a/instanceGroups/gke-prod-default-pool-1a2b3c4d-grp": GKE creates it for a cluster's nodes and deletes it with them
# projects/acme-prod/zones/us-central1-a/instanceGroups/k8s-ig--0a1b2c3d4e5f6a7b: left out by default, would import as "projects/acme-prod/zones/us-central1-a/instanceGroups/k8s-ig--0a1b2c3d4e5f6a7b": a GKE ingress controller creates it for a cluster's nodes and deletes it with the cluster's last ingress
