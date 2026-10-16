# google_compute_subnetwork, from data/google/compute.yaml

import {
  to = google_compute_subnetwork.app
  id = "projects/acme-prod/regions/us-central1/subnetworks/app"
}

# projects/acme-prod/regions/us-central1/subnetworks/default: left out by default, would import as "projects/acme-prod/regions/us-central1/subnetworks/default": a subnetwork of the default network, which Google Cloud creates in each region; select it to import it

# Left out where the plan stays the same, as the default: allow_subnet_cidr_routes_overlap=false, private_ip_google_access=false, private_ipv6_google_access=DISABLE_GOOGLE_ACCESS, purpose=PRIVATE, stack_type=IPV4_ONLY
