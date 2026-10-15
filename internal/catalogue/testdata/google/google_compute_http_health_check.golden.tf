# google_compute_http_health_check, from data/google/compute.yaml

import {
  to = google_compute_http_health_check.legacy
  id = "projects/acme-prod/global/httpHealthChecks/legacy"
}

# projects/acme-prod/global/httpHealthChecks/k8s-be-30080--0a1b2c3d: left out by default, would import as "projects/acme-prod/global/httpHealthChecks/k8s-be-30080--0a1b2c3d": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
