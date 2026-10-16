# google_compute_network, from data/google/compute.yaml

import {
  to = google_compute_network.main
  id = "projects/acme-prod/global/networks/main"
}

# projects/acme-prod/global/networks/default: left out by default, would import as "projects/acme-prod/global/networks/default": the default network Google Cloud creates in each project; select it to import it

# Left out where the plan stays the same, as the default: bgp_always_compare_med=false, bgp_best_path_selection_mode=LEGACY, enable_ula_internal_ipv6=false, mtu=0, network_firewall_policy_enforcement_order=AFTER_CLASSIC_FIREWALL, routing_mode=REGIONAL

# Kept in state only, so a plan may set: delete_bgp_always_compare_med
