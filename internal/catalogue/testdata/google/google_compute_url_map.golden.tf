# google_compute_url_map, from data/google/compute.yaml

import {
  to = google_compute_url_map.web
  id = "projects/acme-prod/global/urlMaps/web"
}

# projects/acme-prod/global/urlMaps/k8s2-um-1a2b3c4d-default-web: left out by default, would import as "projects/acme-prod/global/urlMaps/k8s2-um-1a2b3c4d-default-web": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
