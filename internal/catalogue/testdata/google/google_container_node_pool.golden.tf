# google_container_node_pool, from data/google/container.yaml

import {
  to = google_container_node_pool.legacy_pool
  id = "acme-prod/europe-west1/legacy/pool"
}

import {
  to = google_container_node_pool.prod_default_pool
  id = "acme-prod/us-central1/prod/default-pool"
}

# projects/acme-prod/locations/us-central1/clusters/prod/nodePools/nap-e2-standard-4-1a2b3c4d: left out by default, would import as "acme-prod/us-central1/prod/nap-e2-standard-4-1a2b3c4d": GKE's node auto-provisioning creates it for the cluster's workloads and deletes it when they no longer need it
# projects/acme-prod/locations/us-central1/clusters/us-central1-pipelines-1a2b3c4d-gke/nodePools/default-pool: left out by default, would import as "acme-prod/us-central1/us-central1-pipelines-1a2b3c4d-gke/default-pool": a node pool of a cluster Cloud Composer created, which it deletes with the environment; select it to import it
