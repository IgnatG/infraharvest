# google_compute_region_health_check, from data/google/compute.yaml

import {
  to = google_compute_region_health_check.internal
  id = "projects/acme-prod/regions/us-central1/healthChecks/internal"
}

# projects/acme-prod/regions/us-central1/healthChecks/k8s2-default-api: left out by default, would import as "projects/acme-prod/regions/us-central1/healthChecks/k8s2-default-api": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
