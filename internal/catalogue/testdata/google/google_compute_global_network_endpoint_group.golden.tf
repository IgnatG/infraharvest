# google_compute_global_network_endpoint_group, from data/google/compute.yaml

import {
  to = google_compute_global_network_endpoint_group.external
  id = "projects/acme-prod/global/networkEndpointGroups/external"
}

# projects/acme-prod/global/networkEndpointGroups/k8s1-0a1b2c3d-external: left out by default, would import as "projects/acme-prod/global/networkEndpointGroups/k8s1-0a1b2c3d-external": a GKE ingress or gateway controller creates it and deletes it with the ingress or gateway
