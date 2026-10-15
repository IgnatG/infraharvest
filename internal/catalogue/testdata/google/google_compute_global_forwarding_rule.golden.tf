# google_compute_global_forwarding_rule, from data/google/compute.yaml

import {
  to = google_compute_global_forwarding_rule.web_https
  id = "projects/acme-prod/global/forwardingRules/web-https"
}

# projects/acme-prod/global/forwardingRules/k8s2-fs-1a2b3c4d-default-web: left out by default, would import as "projects/acme-prod/global/forwardingRules/k8s2-fs-1a2b3c4d-default-web": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
