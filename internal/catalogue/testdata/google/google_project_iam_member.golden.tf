# google_project_iam_member, from data/google/iam.yaml

import {
  to = google_project_iam_member.ana_owner
  id = "acme-prod roles/owner user:ana@acme.example"
}

import {
  to = google_project_iam_member.deployer_run_invoker
  id = "acme-prod roles/run.invoker serviceAccount:deployer@acme-prod.iam.gserviceaccount.com"
}

import {
  to = google_project_iam_member.deployer_tokencreator
  id = "acme-prod roles/iam.serviceAccountTokenCreator serviceAccount:deployer@acme-prod.iam.gserviceaccount.com"
}

import {
  to = google_project_iam_member.r_123456789012_compute_storage_admin
  id = "acme-prod roles/storage.admin serviceAccount:123456789012-compute@developer.gserviceaccount.com"
}

# acme-prod roles/pubsub.serviceAgent serviceAccount:service-123456789012@gcp-sa-pubsub.iam.gserviceaccount.com: left out by default, would import as "acme-prod roles/pubsub.serviceAgent serviceAccount:service-123456789012@gcp-sa-pubsub.iam.gserviceaccount.com": a service agent role, which Google Cloud grants a service's agent itself when the service is enabled; select it to import it
# acme-prod roles/compute.instanceGroupManagerServiceAgent serviceAccount:123456789012@cloudservices.gserviceaccount.com: left out by default, would import as "acme-prod roles/compute.instanceGroupManagerServiceAgent serviceAccount:123456789012@cloudservices.gserviceaccount.com": a service agent role, which Google Cloud grants a service's agent itself when the service is enabled; select it to import it
# acme-prod roles/editor serviceAccount:123456789012@cloudservices.gserviceaccount.com: left out by default, would import as "acme-prod roles/editor serviceAccount:123456789012@cloudservices.gserviceaccount.com": the Google APIs service agent's roles/editor, which Google Cloud grants it when the project is created; select it to import it
# acme-prod roles/editor serviceAccount:123456789012-compute@developer.gserviceaccount.com: left out by default, would import as "acme-prod roles/editor serviceAccount:123456789012-compute@developer.gserviceaccount.com": the Compute Engine or App Engine default service account's roles/editor, which Google Cloud grants it when it creates the account; select it to import it
# acme-prod roles/cloudbuild.builds.builder serviceAccount:123456789012@cloudbuild.gserviceaccount.com: left out by default, would import as "acme-prod roles/cloudbuild.builds.builder serviceAccount:123456789012@cloudbuild.gserviceaccount.com": the legacy Cloud Build service account's roles/cloudbuild.builds.builder, which Google Cloud grants it when the Cloud Build API is enabled; select it to import it
# acme-prod roles/iam.serviceAccountTokenCreator serviceAccount:service-123456789012@gcp-sa-pubsub.iam.gserviceaccount.com: left out by default, would import as "acme-prod roles/iam.serviceAccountTokenCreator serviceAccount:service-123456789012@gcp-sa-pubsub.iam.gserviceaccount.com": the Pub/Sub service agent's roles/iam.serviceAccountTokenCreator, which Google Cloud granted it in projects that enabled Pub/Sub before April 2021; select it to import it
# acme-prod roles/viewer deleted:user:bob@acme.example?uid=123456789012345678901: left out by default, would import as "acme-prod roles/viewer deleted:user:bob@acme.example?uid=123456789012345678901": a grant to a deleted principal, which no longer has it; select it to import it
