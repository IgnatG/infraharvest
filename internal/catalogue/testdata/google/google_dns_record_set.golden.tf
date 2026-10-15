# google_dns_record_set, from data/google/dns.yaml

import {
  to = google_dns_record_set.example-com_dev_example_com-NS
  id = "projects/acme-prod/managedZones/example-com/rrsets/dev.example.com./NS"
}

import {
  to = google_dns_record_set.example-com_example_com-MX
  id = "projects/acme-prod/managedZones/example-com/rrsets/example.com./MX"
}

import {
  to = google_dns_record_set.example-com_www_example_com-A
  id = "projects/acme-prod/managedZones/example-com/rrsets/www.example.com./A"
}

# projects/acme-prod/managedZones/example-com/rrsets/example.com./NS: left out by default, would import as "projects/acme-prod/managedZones/example-com/rrsets/example.com./NS": the zone's own name servers, which Cloud DNS creates and deletes with the zone; select it to import it
# projects/acme-prod/managedZones/example-com/rrsets/example.com./SOA: left out by default, would import as "projects/acme-prod/managedZones/example-com/rrsets/example.com./SOA": the zone's SOA record, which Cloud DNS creates and deletes with the zone; select it to import it
