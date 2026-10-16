# google_compute_firewall, from data/google/compute.yaml

import {
  to = google_compute_firewall.allow_https
  id = "projects/acme-prod/global/firewalls/allow-https"
}

# projects/acme-prod/global/firewalls/default-allow-ssh: left out by default, would import as "projects/acme-prod/global/firewalls/default-allow-ssh": a rule of the default network, which Google Cloud creates with it; select it to import it
# projects/acme-prod/global/firewalls/gke-prod-1a2b3c4d-all: left out by default, would import as "projects/acme-prod/global/firewalls/gke-prod-1a2b3c4d-all": GKE creates it for a cluster and deletes it with the cluster
# projects/acme-prod/global/firewalls/k8s-fw-a0b1c2d3e4f5: left out by default, would import as "projects/acme-prod/global/firewalls/k8s-fw-a0b1c2d3e4f5": GKE creates it for a cluster and deletes it with the cluster

# Left out where the plan stays the same, as the default: direction=INGRESS, disabled=false, priority=1000
