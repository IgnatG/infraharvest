# google_pubsub_topic, from data/google/pubsub.yaml

import {
  to = google_pubsub_topic.cloud_builds
  id = "projects/acme-prod/topics/cloud-builds"
}

import {
  to = google_pubsub_topic.gcf_uploads
  id = "projects/acme-prod/topics/gcf-uploads"
}

import {
  to = google_pubsub_topic.orders
  id = "projects/acme-prod/topics/orders"
}

# projects/acme-prod/topics/eventarc-us-central1-thumbnail-123456-347: left out by default, would import as "projects/acme-prod/topics/eventarc-us-central1-thumbnail-123456-347": Eventarc creates it for a trigger, such as a 2nd gen function's, and deletes it with the trigger
# projects/acme-prod/topics/container-analysis-occurrences-v1: left out by default, would import as "projects/acme-prod/topics/container-analysis-occurrences-v1": Artifact Analysis creates it for its notes and occurrences when the Container Analysis API is enabled; select it to import it
