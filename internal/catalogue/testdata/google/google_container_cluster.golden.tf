# google_container_cluster, from data/google/container.yaml

import {
  to = google_container_cluster.dev
  id = "projects/acme-prod/locations/us-central1-a/clusters/dev"
}

import {
  to = google_container_cluster.legacy
  id = "legacy"
}

import {
  to = google_container_cluster.prod
  id = "projects/acme-prod/locations/us-central1/clusters/prod"
}

# projects/acme-prod/locations/us-central1/clusters/us-central1-pipelines-1a2b3c4d-gke: left out by default, would import as "projects/acme-prod/locations/us-central1/clusters/us-central1-pipelines-1a2b3c4d-gke": Cloud Composer creates it for an environment and deletes it with the environment; select it to import it

# Left out of generated configuration: deletion_policy=DELETE, node_pool, node_config
