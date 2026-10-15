# google_artifact_registry_repository, from data/google/serverless.yaml

import {
  to = google_artifact_registry_repository.images
  id = "projects/acme-prod/locations/us-central1/repositories/images"
}

# projects/acme-prod/locations/us-central1/repositories/gcf-artifacts: left out by default, would import as "projects/acme-prod/locations/us-central1/repositories/gcf-artifacts": Cloud Functions creates it for the functions' images
# projects/acme-prod/locations/us-central1/repositories/cloud-run-source-deploy: left out by default, would import as "projects/acme-prod/locations/us-central1/repositories/cloud-run-source-deploy": gcloud creates it to deploy Cloud Run services and functions from source
