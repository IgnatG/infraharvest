# google_dns_managed_zone, from data/google/dns.yaml

import {
  to = google_dns_managed_zone.internal
  id = "projects/acme-prod/managedZones/internal"
}

import {
  to = google_dns_managed_zone.public
  id = "public"
}

# Left out where the plan stays the same, as the default: cloud_logging_config.enable_logging=false, force_destroy=false, visibility=public
