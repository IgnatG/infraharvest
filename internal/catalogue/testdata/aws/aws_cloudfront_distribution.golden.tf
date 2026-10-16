# aws_cloudfront_distribution, from data/aws/cloudfront.yaml

import {
  to = aws_cloudfront_distribution.site
  id = "E1A2B3C4D5E6F7"
}

# Left out of generated configuration: region, tags_all, default_cache_behavior.default_ttl when default_cache_behavior.cache_policy_id and !default_cache_behavior.cache_policy_id=, default_cache_behavior.max_ttl when default_cache_behavior.cache_policy_id and !default_cache_behavior.cache_policy_id=, ordered_cache_behavior.default_ttl when ordered_cache_behavior.cache_policy_id and !ordered_cache_behavior.cache_policy_id=, ordered_cache_behavior.max_ttl when ordered_cache_behavior.cache_policy_id and !ordered_cache_behavior.cache_policy_id=, viewer_certificate.minimum_protocol_version when viewer_certificate.cloudfront_default_certificate=true

# Generated configuration, without the arguments the catalogue omits:
resource "aws_cloudfront_distribution" "site" {
  enabled         = true
  http_version    = "http2"
  is_ipv6_enabled = true
  price_class     = "PriceClass_100"
  default_cache_behavior {
    allowed_methods        = ["GET", "HEAD"]
    cache_policy_id        = "658327ea-f89d-4fab-a63d-7e88639e58f6"
    cached_methods         = ["GET", "HEAD"]
    compress               = true
    min_ttl                = 0
    target_origin_id       = "site"
    viewer_protocol_policy = "redirect-to-https"
  }
  ordered_cache_behavior {
    allowed_methods        = ["GET", "HEAD"]
    cache_policy_id        = ""
    cached_methods         = ["GET", "HEAD"]
    default_ttl            = 3600
    max_ttl                = 86400
    min_ttl                = 0
    path_pattern           = "/legacy/*"
    target_origin_id       = "site"
    viewer_protocol_policy = "redirect-to-https"
    forwarded_values {
      query_string = false
      cookies {
        forward = "none"
      }
    }
  }
  origin {
    domain_name = "site.s3.eu-west-2.amazonaws.com"
    origin_id   = "site"
  }
  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }
  viewer_certificate {
    cloudfront_default_certificate = true
  }
}
