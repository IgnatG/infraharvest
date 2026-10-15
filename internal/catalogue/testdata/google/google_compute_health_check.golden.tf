# google_compute_health_check, from data/google/compute.yaml

import {
  to = google_compute_health_check.web
  id = "projects/acme-prod/global/healthChecks/web"
}

# projects/acme-prod/global/healthChecks/k8s1-0a1b2c3d-default-web-80: left out by default, would import as "projects/acme-prod/global/healthChecks/k8s1-0a1b2c3d-default-web-80": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
