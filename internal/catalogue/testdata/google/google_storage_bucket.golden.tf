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
# acme-prod_cloudbuild: left out by default, would import as "acme-prod_cloudbuild": Cloud Build creates it, as <project>_cloudbuild, for the sources gcloud builds submit uploads
# artifacts.acme-prod.appspot.com: left out by default, would import as "artifacts.acme-prod.appspot.com": Container Registry creates it for the project's images
# eu.artifacts.acme-prod.appspot.com: left out by default, would import as "eu.artifacts.acme-prod.appspot.com": Container Registry creates it for the project's images
# acme-prod.appspot.com: left out by default, would import as "acme-prod.appspot.com": App Engine creates it, as <project>.appspot.com or staging.<project>.appspot.com, with the project's app
# staging.acme-prod.appspot.com: left out by default, would import as "staging.acme-prod.appspot.com": App Engine creates it, as <project>.appspot.com or staging.<project>.appspot.com, with the project's app
# us-central1-pipelines-1a2b3c4d-bucket: left out by default, would import as "us-central1-pipelines-1a2b3c4d-bucket": Cloud Composer creates it for an environment's DAGs, plugins and data; select it to import it

# Left out where the plan stays the same, as the default: default_event_based_hold=false, enable_object_retention=false, force_destroy=false, hierarchical_namespace.enabled=false, public_access_prevention=inherited, requester_pays=false, soft_delete_policy.retention_duration_seconds=604800, storage_class=STANDARD, uniform_bucket_level_access=false
