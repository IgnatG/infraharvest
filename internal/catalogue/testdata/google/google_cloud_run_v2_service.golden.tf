# google_cloud_run_v2_service, from data/google/serverless.yaml

import {
  to = google_cloud_run_v2_service.web
  id = "projects/acme-prod/locations/us-central1/services/web"
}

# projects/acme-prod/locations/us-central1/services/thumbnail: left out by default, would import as "projects/acme-prod/locations/us-central1/services/thumbnail": the service a 2nd gen function runs on, which Cloud Functions creates and deletes with the function, and google_cloudfunctions2_function imports
