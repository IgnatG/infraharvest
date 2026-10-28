# Resources the Google Cloud environments sandbox test
# (TestSandboxGoogleEnvironments, in sandbox_google_environments_test.go)
# creates in two real projects before importing them: a Pub/Sub topic of
# dev in the first sandbox project and one of prd in the second, as the
# configuration file's environments map them by project. Their owner
# label differs. prod_alerts adds prd's alerts topic, for the
# incremental import, and prod_owner changes prd's owner label, for
# drift. Each project's fixture service account creates what is in it,
# as only it may. A topic without messages costs nothing, and the estate
# is destroyed after each run.

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same projects, such as one per engine."
  type        = string
  default     = "terraform"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{0,8}$", var.suffix))
    error_message = "Use up to nine lower-case letters and digits, starting with a letter."
  }
}

variable "dev_project" {
  description = "The first sandbox project, dev's."
  type        = string
}

variable "dev_credentials" {
  description = "The credentials file of the first project's fixture service account."
  type        = string
}

variable "prod_project" {
  description = "The second sandbox project, prd's."
  type        = string
}

variable "prod_credentials" {
  description = "The credentials file of the second project's fixture service account."
  type        = string
}

variable "prod_alerts" {
  description = "Whether prd has an alerts topic, which the test creates once the merged root is applied, for prod's incremental import."
  type        = bool
  default     = false
}

variable "prod_owner" {
  description = "The owner label of prd's orders topic, which the test changes out of band of the applied merged root, for drift."
  type        = string
  default     = "team-b"
}

# Without the goog-terraform-provisioned label, which tells infraharvest
# another state owns a resource: the import is what is tested.
provider "google" {
  alias       = "dev"
  project     = var.dev_project
  credentials = var.dev_credentials

  add_terraform_attribution_label = false
}

provider "google" {
  alias       = "prod"
  project     = var.prod_project
  credentials = var.prod_credentials

  add_terraform_attribution_label = false
}

locals {
  # Everything a run creates is named after the prefix and carries the
  # labels, so a sweep finds what a run left.
  name   = "ih-sbx-${var.suffix}"
  labels = { project = "infraharvest-sandbox", infraharvest-sandbox = var.suffix }
}

resource "google_pubsub_topic" "dev" {
  provider = google.dev
  name     = "${local.name}-dev-orders"
  labels   = merge(local.labels, { owner = "team-a" })
}

resource "google_pubsub_topic" "prod" {
  provider = google.prod
  name     = "${local.name}-prd-orders"
  labels   = merge(local.labels, { owner = var.prod_owner })
}

resource "google_pubsub_topic" "prod_alerts" {
  count    = var.prod_alerts ? 1 : 0
  provider = google.prod
  name     = "${local.name}-prd-alerts"
  labels   = local.labels
}

output "name" {
  description = "What every resource's name starts with: the test waits for Cloud Asset Inventory to find them."
  value       = local.name
}
