# google_compute_network_endpoint_group, from data/google/compute.yaml

import {
  to = google_compute_network_endpoint_group.web
  id = "projects/acme-prod/zones/us-central1-a/networkEndpointGroups/web"
}

# projects/acme-prod/zones/us-central1-a/networkEndpointGroups/k8s1-0a1b2c3d-default-web-80-4f5e6a7b: left out by default, would import as "projects/acme-prod/zones/us-central1-a/networkEndpointGroups/k8s1-0a1b2c3d-default-web-80-4f5e6a7b": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
