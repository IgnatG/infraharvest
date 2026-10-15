# google_cloudbuild_trigger, from data/google/cloudbuild.yaml

import {
  to = google_cloudbuild_trigger.deploy_web
  id = "projects/acme-prod/locations/global/triggers/0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
}

import {
  to = google_cloudbuild_trigger.legacy
  id = "a1b2c3d4-e5f6-a7b8-c9d0-e1f2a3b4c5d6"
}

import {
  to = google_cloudbuild_trigger.nightly
  id = "projects/acme-prod/locations/global/triggers/9a8b7c6d-5e4f-3a2b-1c0d-e9f8a7b6c5d4"
}

import {
  to = google_cloudbuild_trigger.release
  id = "projects/acme-prod/triggers/11223344-5566-7788-99aa-bbccddeeff00"
}
