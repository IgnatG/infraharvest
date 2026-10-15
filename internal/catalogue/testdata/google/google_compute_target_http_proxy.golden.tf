# google_compute_target_http_proxy, from data/google/compute.yaml

import {
  to = google_compute_target_http_proxy.web
  id = "projects/acme-prod/global/targetHttpProxies/web"
}

# projects/acme-prod/global/targetHttpProxies/k8s2-tp-1a2b3c4d-default-web: left out by default, would import as "projects/acme-prod/global/targetHttpProxies/k8s2-tp-1a2b3c4d-default-web": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
