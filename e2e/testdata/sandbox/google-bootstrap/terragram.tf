# What Terragram's own Google Cloud sandbox workflow (sandbox-google.yml in
# the Terragram repository) needs in the project, off unless
# terragram_enabled is set: Terragram applies and destroys the Google
# Cloud plans its emit gate generates, in this project, as a service
# account of its own, which only that repository's sandbox environment
# can sign in as. Nothing here changes infraharvest's own pool, provider or
# service accounts, which keep their conditions, and none of infraharvest's
# service accounts trusts it.
#
# - terragram-github, a workload identity pool of its own, with a provider
#   that accepts the GitHub OIDC tokens of the Terragram repository's
#   sandbox environment, run by that workflow file, and only those: the
#   repository's and its owner's immutable IDs, the environment and the
#   workflow are its condition, so no other repository, fork, environment
#   or workflow gets in, whatever its name;
# - terragram-ci-sandbox, a service account that creates and destroys the
#   plans' resources, with the predefined roles that manage their kinds of
#   resources in the project, and nothing else. No role of it can change a
#   workload identity pool, a service account's IAM policy or the project's
#   IAM policy beyond the six roles its plans grant their own service
#   accounts (see terragram_grantable_roles).

locals {
  terragram = var.terragram_enabled ? 1 : 0

  # The Terragram service account's roles: the narrowest predefined roles
  # that create and delete the kinds of resources the four plans hold
  # (scripts/emit-plans.ts in the Terragram repository). Google Cloud
  # can't limit a role to names under a prefix, so they apply to the
  # whole project, which is why the sandbox should have a project of its
  # own, as the fixture's do.
  terragram_roles = toset([
    # Enabling the APIs the plans ask for (google_project_service), and
    # their service agents (google_project_service_identity).
    "roles/serviceusage.serviceUsageAdmin",
    # Networks, subnetworks, routers and NAT, addresses, the load balancer
    # (forwarding rules, proxies, URL maps, backend services and buckets,
    # network endpoint groups), but not firewall rules or certificates.
    "roles/compute.networkAdmin",
    # Firewall rules, Cloud Armor security policies, TLS policies and
    # managed certificates.
    "roles/compute.securityAdmin",
    # The virtual machine and its disk.
    "roles/compute.instanceAdmin.v1",
    # The private service connection Cloud SQL and Memorystore use.
    "roles/servicenetworking.networksAdmin",
    # The GKE cluster and its node pool.
    "roles/container.admin",
    # Cloud Run services and, as the plans grant roles/run.invoker on
    # them, their IAM policies: Cloud Run Developer can't.
    "roles/run.admin",
    # The function, whose Cloud Run service Cloud Functions creates and
    # deletes itself.
    "roles/cloudfunctions.developer",
    # The managed zone and its record set.
    "roles/dns.admin",
    # Topics and subscriptions, without the IAM policies on them.
    "roles/pubsub.editor",
    # Buckets and their IAM policies: no narrower predefined role creates
    # and deletes them.
    "roles/storage.admin",
    # Cloud SQL instances, databases and users.
    "roles/cloudsql.admin",
    # BigQuery datasets: Data Editor can't delete a dataset.
    "roles/bigquery.dataOwner",
    # The Memorystore instance.
    "roles/redis.admin",
    # The Firestore database.
    "roles/datastore.owner",
    # Secrets (the plans create none with a version) and their key.
    "roles/secretmanager.admin",
    "roles/cloudkms.admin",
    # The log bucket's configuration.
    "roles/logging.configWriter",
    # The alert policy and its notification channel.
    "roles/monitoring.alertPolicyEditor",
    "roles/monitoring.notificationChannelEditor",
    # Service accounts: create, read and delete them, but neither create
    # keys for them nor change who may use them, as serviceAccountAdmin
    # could.
    "roles/iam.serviceAccountCreator",
    "roles/iam.serviceAccountDeleter",
    "roles/iam.serviceAccountViewer",
    # Attaching the plans' service accounts to the virtual machine, the
    # nodes, Cloud Run, the function and the build. Google Cloud can't
    # limit it to service accounts by name, so it holds for every service
    # account in the project, infraharvest's too (see the README).
    "roles/iam.serviceAccountUser",
    # Reading what planning reads: the project, and its IAM policy.
    "roles/iam.securityReviewer",
    "roles/viewer",
  ])

  # The roles the plans grant their own service accounts in the project's
  # IAM policy (log, metric and Artifact Registry reading, and Cloud SQL
  # client and instance user). The Terragram account may change the
  # project's IAM policy for these alone: a request that grants or
  # revokes any other role is refused, so it can't grant itself or
  # anyone else more, or take anyone's roles away.
  terragram_grantable_roles = [
    "roles/artifactregistry.reader",
    "roles/cloudsql.client",
    "roles/cloudsql.instanceUser",
    "roles/logging.logWriter",
    "roles/monitoring.metricWriter",
    "roles/monitoring.viewer",
  ]

  terragram_workflow_ref = "${var.terragram_repository}/${var.terragram_workflow}@"
}

resource "google_iam_workload_identity_pool" "terragram" {
  count = local.terragram

  workload_identity_pool_id = var.terragram_pool_id
  display_name              = "Terragram GitHub"
  description               = "GitHub Actions jobs of the Terragram sandbox environment."

  depends_on = [google_project_service.enabled]
}

# Accepts the tokens of the Terragram repository's sandbox environment,
# from its sandbox workflow, by the repository's and its owner's
# immutable IDs, and no others.
resource "google_iam_workload_identity_pool_provider" "terragram" {
  count = local.terragram

  workload_identity_pool_id          = google_iam_workload_identity_pool.terragram[0].workload_identity_pool_id
  workload_identity_pool_provider_id = "github-actions"
  display_name                       = "GitHub Actions"
  description                        = "GitHub's OIDC tokens for the ${var.terragram_environment} environment of ${var.terragram_repository}."

  attribute_mapping = {
    "google.subject"                = "assertion.sub"
    "attribute.repository_id"       = "assertion.repository_id"
    "attribute.repository_owner_id" = "assertion.repository_owner_id"
    "attribute.environment"         = "assertion.environment"
  }
  attribute_condition = join(" && ", [
    "assertion.repository_id == '${var.terragram_repository_id}'",
    "assertion.repository_owner_id == '${var.terragram_repository_owner_id}'",
    "assertion.environment == '${var.terragram_environment}'",
    "assertion.job_workflow_ref.startsWith('${local.terragram_workflow_ref}')",
  ])

  oidc {
    issuer_uri = local.github_issuer
  }
}

# Creates and destroys what the Terragram plans hold.
resource "google_service_account" "terragram" {
  count = local.terragram

  account_id   = "terragram-ci-sandbox"
  display_name = "Terragram CI sandbox"
  description  = "Terragram sandbox: creates and destroys what its Google Cloud plans hold."

  depends_on = [google_project_service.enabled]
}

resource "google_project_iam_member" "terragram" {
  # checkov:skip=CKV_GCP_117:roles/viewer is the read-only access planning needs, granted on purpose
  # checkov:skip=CKV_GCP_49:serviceAccountUser, Creator and Deleter can't be limited to the plans' service accounts by name, and the sandbox project holds nothing else
  # checkov:skip=CKV_GCP_41:serviceAccountUser at the project is what attaching the plans' service accounts takes, in a project of its own
  for_each = var.terragram_enabled ? local.terragram_roles : toset([])

  project = var.project_id
  role    = each.value
  member  = google_service_account.terragram[0].member
}

# Changing the project's IAM policy, for the roles above alone.
resource "google_project_iam_member" "terragram_iam" {
  count = local.terragram

  project = var.project_id
  role    = "roles/resourcemanager.projectIamAdmin"
  member  = google_service_account.terragram[0].member

  condition {
    title       = "terragram-sandbox-grantable-roles"
    description = "Only the roles the Terragram plans grant their own service accounts."
    expression  = "api.getAttribute('iam.googleapis.com/modifiedGrantsByRole', []).hasOnly([${join(", ", formatlist("'%s'", local.terragram_grantable_roles))}])"
  }
}

# The sandbox environment's jobs, of that workflow, may impersonate the
# service account. The pool's provider has already refused every other
# token, so the repository's immutable ID is all the binding names.
resource "google_service_account_iam_member" "terragram_github" {
  count = local.terragram

  service_account_id = google_service_account.terragram[0].name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.terragram[0].name}/attribute.repository_id/${var.terragram_repository_id}"
}
