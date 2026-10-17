# What the Google Cloud sandbox (the sandbox-google workflow,
# TestSandboxGoogle in e2e/sandbox_google_test.go) needs in a project. Its
# owner applies it once, in Google Cloud Shell, signed in as an Owner of
# the project (see e2e/README.md):
#
# - the APIs the estate, the import and the sign-in use;
# - a workload identity pool, with a provider that accepts the GitHub OIDC
#   tokens of the repository's sandbox environment, and only those: the
#   repository's and its owner's immutable IDs and the environment are its
#   condition, so no other repository's tokens get in, whatever its name;
# - infraharvest-ci-fixture, a service account that creates and destroys
#   the estate, with the predefined roles that manage the estate's kinds
#   of resources in the project, and nothing else;
# - infraharvest-ci-function, a service account that Cloud Build builds
#   the estate's function as and the function runs as, which the fixture
#   may deploy it as;
# - infraharvest-ci-readonly, a service account that imports the estate.
#   permissions/google, the template users apply, grants it what it grants
#   users: Viewer, Security Reviewer and Cloud Asset Viewer on the project.
#
# The sandbox environment's jobs, and only they, may impersonate the two
# service accounts: no key is created or stored.

provider "google" {
  project = var.project_id

  # Enabling APIs with a user's credentials, such as Cloud Shell's, needs a
  # quota project: the sandbox project itself.
  billing_project       = var.project_id
  user_project_override = true
}

locals {
  github_issuer = "https://token.actions.githubusercontent.com"
  # Whom the service accounts trust: the pool's identity for the
  # environment's subject. The pool's name holds the project's number.
  principal = "principal://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/subject/${var.github_subject}"

  # What the estate creates (compute, dns, pubsub, storage, iam, and
  # cloudfunctions, with run, cloudbuild and artifactregistry, which a 2nd
  # gen function runs on and is built with, and logging, which its build
  # logs go to); what the import reads: cloudasset, cloudresourcemanager,
  # cloudbilling, which the plan of the imported project reads its billing
  # account from, and sqladmin and cloudscheduler, so that their services
  # find nothing to list rather than the APIs disabled; and what the
  # sign-in uses (sts, iamcredentials). serviceusage, which enables the
  # rest, is enabled in new projects.
  # Enabled first: enabling any other API goes through these two.
  foundation_services = toset([
    "cloudresourcemanager.googleapis.com",
    "serviceusage.googleapis.com",
  ])

  services = toset([
    "artifactregistry.googleapis.com",
    "cloudasset.googleapis.com",
    "cloudbilling.googleapis.com",
    "cloudbuild.googleapis.com",
    "cloudfunctions.googleapis.com",
    "cloudscheduler.googleapis.com",
    "compute.googleapis.com",
    "dns.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "logging.googleapis.com",
    "pubsub.googleapis.com",
    "run.googleapis.com",
    "sqladmin.googleapis.com",
    "storage.googleapis.com",
    "sts.googleapis.com",
  ])

  # The fixture's roles: the narrowest predefined roles that create and
  # delete the estate's kinds of resources. Google Cloud can't limit a
  # role to names under a prefix, so they apply to the whole project,
  # which is why the sandbox should have a project of its own.
  fixture_roles = toset([
    # Functions: deploy and delete them, as infraharvest-ci-function,
    # which the fixture may act as (see fixture_function below). Cloud
    # Functions creates and deletes the Cloud Run service itself.
    "roles/cloudfunctions.developer",
    # Networks, subnetworks and routers, but not firewall rules.
    "roles/compute.networkAdmin",
    # Firewall rules.
    "roles/compute.securityAdmin",
    # Instances and disks; the instance has no service account, so the
    # fixture needn't act as one.
    "roles/compute.instanceAdmin.v1",
    # Managed zones and record sets, and binding a private zone to a
    # network.
    "roles/dns.admin",
    # Topics and subscriptions, without the IAM policies on them, which
    # pubsub.admin could change.
    "roles/pubsub.editor",
    # Buckets: no narrower predefined role creates and deletes them.
    "roles/storage.admin",
    # Service accounts: create, read and delete them, but neither create
    # keys for them nor change who may use them, as serviceAccountAdmin
    # could.
    "roles/iam.serviceAccountCreator",
    "roles/iam.serviceAccountDeleter",
    "roles/iam.serviceAccountViewer",
  ])

  # What infraharvest-ci-function needs to build the function, as a
  # custom build service account, and nothing to run it: write the
  # build's logs, push its image and read its source.
  function_roles = toset([
    "roles/artifactregistry.writer",
    "roles/logging.logWriter",
    "roles/storage.objectViewer",
  ])
}

# Left enabled when the bootstrap is destroyed: other things in the
# project may use them.
resource "google_project_service" "foundation" {
  for_each = local.foundation_services

  service            = each.value
  disable_on_destroy = false
}

# A newly enabled API takes a minute or so to be usable.
resource "time_sleep" "foundation" {
  create_duration = "90s"

  depends_on = [google_project_service.foundation]
}

resource "google_project_service" "enabled" {
  for_each = local.services

  service            = each.value
  disable_on_destroy = false

  depends_on = [time_sleep.foundation]
}

moved {
  from = google_project_service.enabled["cloudresourcemanager.googleapis.com"]
  to   = google_project_service.foundation["cloudresourcemanager.googleapis.com"]
}

moved {
  from = google_project_service.enabled["serviceusage.googleapis.com"]
  to   = google_project_service.foundation["serviceusage.googleapis.com"]
}

resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = var.pool_id
  display_name              = "infraharvest GitHub"
  description               = "GitHub Actions jobs of the infraharvest sandbox environment."

  depends_on = [google_project_service.enabled]
}

# Accepts the tokens of the repository's sandbox environment alone, by the
# repository's and its owner's immutable IDs. A token's subject becomes
# the principal's, which the service accounts trust.
resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github-actions"
  display_name                       = "GitHub Actions"
  description                        = "GitHub's OIDC tokens for the sandbox environment of ${var.github_repository}."

  attribute_mapping = {
    "google.subject"                = "assertion.sub"
    "attribute.repository_id"       = "assertion.repository_id"
    "attribute.repository_owner_id" = "assertion.repository_owner_id"
    "attribute.environment"         = "assertion.environment"
  }
  attribute_condition = join(" && ", [
    "assertion.repository_id == '${var.github_repository_id}'",
    "assertion.repository_owner_id == '${var.github_repository_owner_id}'",
    "assertion.environment == 'sandbox'",
  ])

  oidc {
    issuer_uri = local.github_issuer
  }
}

# Creates and destroys the estate.
resource "google_service_account" "fixture" {
  account_id   = "infraharvest-ci-fixture"
  display_name = "infraharvest CI fixture"
  description  = "infraharvest sandbox: creates and destroys the estate."

  depends_on = [google_project_service.enabled]
}

# Imports the estate, with what permissions/google grants.
resource "google_service_account" "readonly" {
  account_id   = "infraharvest-ci-readonly"
  display_name = "infraharvest CI read-only"
  description  = "infraharvest sandbox: imports the estate, with what permissions/google grants."

  # The template's grants come after the APIs through the account's
  # email: a module with a provider of its own can't take depends_on.
  depends_on = [google_project_service.enabled]
}

resource "google_project_iam_member" "fixture" {
  for_each = local.fixture_roles

  project = var.project_id
  role    = each.value
  member  = google_service_account.fixture.member
}

# Builds the estate's function, as its custom build service account, and
# runs it: since July 2024, a new project's default Cloud Build service
# account may lack what building a function needs.
resource "google_service_account" "function" {
  account_id   = "infraharvest-ci-function"
  display_name = "infraharvest CI function"
  description  = "infraharvest sandbox: builds and runs the estate's function."

  depends_on = [google_project_service.enabled]
}

resource "google_project_iam_member" "function" {
  for_each = local.function_roles

  project = var.project_id
  role    = each.value
  member  = google_service_account.function.member
}

# Deploying a function as an account needs Service Account User on it:
# the fixture has it on this account alone.
resource "google_service_account_iam_member" "fixture_function" {
  service_account_id = google_service_account.function.name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.fixture.member
}

# The sandbox environment's jobs may impersonate each service account.
resource "google_service_account_iam_member" "fixture_github" {
  service_account_id = google_service_account.fixture.name
  role               = "roles/iam.workloadIdentityUser"
  member             = local.principal
}

resource "google_service_account_iam_member" "readonly_github" {
  service_account_id = google_service_account.readonly.name
  role               = "roles/iam.workloadIdentityUser"
  member             = local.principal
}

# The template users apply, as they apply it: the sandbox validates it.
module "readonly" {
  source = "../../../../permissions/google"

  project_id = var.project_id
  member     = google_service_account.readonly.member
}
