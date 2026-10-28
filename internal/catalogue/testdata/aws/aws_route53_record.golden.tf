# aws_route53_record, from data/aws/route53.yaml

import {
  to = aws_route53_record.api_CNAME_eu
  id = "Z0123456789ABCDEFGHIJ_api.example.internal_CNAME_eu"
}

import {
  to = aws_route53_record.dev_NS
  id = "Z0123456789ABCDEFGHIJ_dev.example.internal_NS"
}

import {
  to = aws_route53_record.example_internal_MX
  id = "Z0123456789ABCDEFGHIJ_example.internal_MX"
}

import {
  to = aws_route53_record.www_A
  id = "Z0123456789ABCDEFGHIJ_www.example.internal_A"
}

# Z0123456789ABCDEFGHIJ_example.internal._NS_: left out by default, would import as "Z0123456789ABCDEFGHIJ_example.internal_NS": the zone's own name servers, which Route 53 creates and deletes with the zone; select it to import it
# Z0123456789ABCDEFGHIJ_example.internal._SOA_: left out by default, would import as "Z0123456789ABCDEFGHIJ_example.internal_SOA": the zone's SOA record, which Route 53 creates and deletes with the zone; select it to import it
