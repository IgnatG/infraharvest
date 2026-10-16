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
    default_ttl            = 0
    max_ttl                = 0
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
    minimum_protocol_version       = "TLSv1"
  }
}
