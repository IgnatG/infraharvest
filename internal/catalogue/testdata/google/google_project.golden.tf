# google_project, from data/google/project.yaml

import {
  to = google_project.acme_legacy
  id = "acme-legacy"
}

import {
  to = google_project.acme_prod
  id = "acme-prod"
}

# Left out of generated configuration: deletion_policy=DELETE, deletion_policy=PREVENT

# Left out where the plan stays the same, as the default: auto_create_network=true
