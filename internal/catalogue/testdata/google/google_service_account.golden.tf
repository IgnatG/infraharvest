# google_service_account, from data/google/iam.yaml

import {
  to = google_service_account.ci
  id = "projects/acme-prod/serviceAccounts/ci@acme-prod.iam.gserviceaccount.com"
}

import {
  to = google_service_account.deployer
  id = "projects/acme-prod/serviceAccounts/deployer@acme-prod.iam.gserviceaccount.com"
}

# projects/acme-prod/serviceAccounts/104355798712345678902: left out by default, would import as "projects/acme-prod/serviceAccounts/123456789012-compute@developer.gserviceaccount.com": the Compute Engine default service account, which GCP creates when the Compute Engine API is enabled; select it to import it
# projects/acme-prod/serviceAccounts/104355798712345678903: left out by default, would import as "projects/acme-prod/serviceAccounts/acme-prod@appspot.gserviceaccount.com": the App Engine default service account, which GCP creates with an App Engine app; select it to import it
