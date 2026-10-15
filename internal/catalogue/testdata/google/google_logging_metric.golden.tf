# google_logging_metric, from data/google/observability.yaml

import {
  to = google_logging_metric.app_latency
  id = "acme-prod app/latency"
}

import {
  to = google_logging_metric.errors
  id = "acme-prod errors"
}

import {
  to = google_logging_metric.legacy
  id = "legacy"
}
