# google_compute_forwarding_rule, from data/google/compute.yaml

import {
  to = google_compute_forwarding_rule.internal_lb
  id = "projects/acme-prod/regions/us-central1/forwardingRules/internal-lb"
}

# projects/acme-prod/regions/us-central1/forwardingRules/k8s2-fr-1a2b3c4d-default-api: left out by default, would import as "projects/acme-prod/regions/us-central1/forwardingRules/k8s2-fr-1a2b3c4d-default-api": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
