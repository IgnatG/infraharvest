# google_storage_bucket, from data/google/gcs.yaml

import {
  to = google_storage_bucket.acme_prod_assets
  id = "acme-prod-assets"
}

import {
  to = google_storage_bucket.legacy_bucket
  id = "legacy-bucket"
}

# gcf-sources-123456789012-us-central1: left out by default, would import as "gcf-sources-123456789012-us-central1": Cloud Functions, Cloud Run or Dataproc creates it for its own use
# dataproc-staging-us-central1-123456789012-abcd1234: left out by default, would import as "dataproc-staging-us-central1-123456789012-abcd1234": Cloud Functions, Cloud Run or Dataproc creates it for its own use
