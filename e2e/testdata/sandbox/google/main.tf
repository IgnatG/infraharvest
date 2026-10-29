# Resources the Google Cloud sandbox test (sandbox_google_test.go) creates
# in a real project before importing them: what floci-gcp can't cover,
# such as real Cloud Asset Inventory search results, Cloud DNS zones and
# record sets, a Cloud Router, a stopped instance with a boot disk and a
# data disk, a 2nd gen function, with what Google creates for it, a Cloud
# Run service behind a global external load balancer, a Cloud SQL for
# PostgreSQL instance, a BigQuery dataset and table, a KMS key, a secret,
# an Artifact Registry repository and a Cloud Scheduler job, and whether
# the read-only template (permissions/google) grants enough. The project
# and its APIs come from the sandbox bootstrap
# (testdata/sandbox/google-bootstrap), which lets the fixture service
# account manage these kinds of resources: nothing bills by the hour but
# two small disks, the database, and the load balancer's forwarding rule
# and address, about $0.07 an hour in all, and it is destroyed after each
# run, but for the KMS key ring and key, which can't be deleted.
# The provider signs in with the credentials file the test names in
# GOOGLE_APPLICATION_CREDENTIALS: the fixture service account's.

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.7"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same project, such as one per engine."
  type        = string
  default     = "terraform"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{0,8}$", var.suffix))
    error_message = "Use up to nine lower-case letters and digits, starting with a letter: the suffix goes into the service account's ID, which is at most 30 characters."
  }
}

variable "project" {
  description = "The sandbox project, which the bootstrap prepares."
  type        = string
}

variable "region" {
  description = "Where the regional resources are: the subnetworks, the router, the instance and its disks (in the region's zone a), the bucket, the function, the Cloud Run service and its network endpoint group, the database, the dataset, the key ring, the repository and the scheduler job."
  type        = string
  default     = "europe-west2"
}

variable "drift_label" {
  description = "Another value of the topic's project label, which a second apply sets out of band of the applied root for infraharvest drift to find. A label the code doesn't set is the provider's to ignore, so the change is to one it sets."
  type        = string
  default     = ""
}

provider "google" {
  project = var.project
  region  = var.region

  # Without the goog-terraform-provisioned label, which tells infraharvest
  # another state owns a resource: the import is what is tested.
  add_terraform_attribution_label = false
}

# Names no other run has used, as bucket names must be unique across
# Google Cloud.
resource "random_string" "id" {
  length  = 6
  upper   = false
  special = false
}

locals {
  # Everything a run creates is named after the prefix, and everything
  # that takes labels carries them, so a sweep finds what a run left.
  # Networks, subnetworks, firewall rules, routers and service accounts
  # take no labels: the sweep finds them by name.
  name   = "ih-sbx-${var.suffix}-${random_string.id.result}"
  labels = { project = "infraharvest-sandbox", infraharvest-sandbox = var.suffix }
}

output "name" {
  description = "What every resource's name starts with: the test waits for Cloud Asset Inventory to find them."
  value       = local.name
}

resource "google_compute_network" "main" {
  name                    = local.name
  description             = "infraharvest sandbox"
  auto_create_subnetworks = false
  routing_mode            = "REGIONAL"
}

resource "google_compute_subnetwork" "app" {
  name                     = "${local.name}-app"
  region                   = var.region
  network                  = google_compute_network.main.id
  ip_cidr_range            = "10.80.1.0/24"
  private_ip_google_access = true
}

resource "google_compute_subnetwork" "data" {
  name          = "${local.name}-data"
  region        = var.region
  network       = google_compute_network.main.id
  ip_cidr_range = "10.80.2.0/24"
}

resource "google_compute_firewall" "https" {
  name          = "${local.name}-https"
  network       = google_compute_network.main.id
  direction     = "INGRESS"
  source_ranges = ["10.80.0.0/16"]
  target_tags   = ["app"]

  allow {
    protocol = "tcp"
    ports    = ["443"]
  }
}

# A type no lister lists, which the test lists through the query service
# (Terraform's list resources). A global network firewall policy with no
# rules and no network costs nothing.
resource "google_compute_network_firewall_policy" "query" {
  name        = "${local.name}-policy"
  description = "infraharvest sandbox"
}

# A Cloud Router without NAT or BGP peers, which costs nothing.
resource "google_compute_router" "main" {
  name    = local.name
  region  = var.region
  network = google_compute_network.main.id

  bgp {
    asn = 64514
  }
}

# A stopped e2-micro instance with a standard boot disk, which Google
# names after the instance, and a standard data disk, each 10 GB, the
# smallest a standard disk can be: a stopped instance bills only its
# disks, a fraction of a cent for the minutes they exist. It has no
# external address and no service account. The default selection must
# leave out the boot disk, which the instance manages in its boot_disk
# block, and the import must import the data disk, which the instance
# attaches by its source.
resource "google_compute_disk" "data" {
  name   = "${local.name}-disk"
  zone   = "${var.region}-a"
  type   = "pd-standard"
  size   = 10
  labels = local.labels
}

resource "google_compute_instance" "app" {
  name           = "${local.name}-vm"
  zone           = "${var.region}-a"
  machine_type   = "e2-micro"
  desired_status = "TERMINATED"
  labels         = local.labels

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      type  = "pd-standard"
      size  = 10
    }
  }

  attached_disk {
    source = google_compute_disk.data.id
  }

  network_interface {
    subnetwork = google_compute_subnetwork.app.id
  }
}

# A bucket that holds nothing but the function's few hundred bytes of
# source costs nothing.
resource "google_storage_bucket" "data" {
  name                        = local.name
  location                    = upper(var.region)
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = true
  labels                      = local.labels
}

# A topic and a subscription with no messages cost nothing.
resource "google_pubsub_topic" "events" {
  name   = local.name
  labels = var.drift_label == "" ? local.labels : merge(local.labels, { project = var.drift_label })
}

resource "google_pubsub_subscription" "events" {
  name                 = local.name
  topic                = google_pubsub_topic.events.id
  ack_deadline_seconds = 20
  labels               = local.labels
}

# A private zone, visible to the network, with a record set: a fraction of
# a cent for the minutes it exists, and no queries.
resource "google_dns_managed_zone" "internal" {
  name        = local.name
  dns_name    = "${local.name}.internal."
  description = "infraharvest sandbox"
  visibility  = "private"
  labels      = local.labels

  private_visibility_config {
    networks {
      network_url = google_compute_network.main.id
    }
  }
}

resource "google_dns_record_set" "app" {
  name         = "app.${google_dns_managed_zone.internal.dns_name}"
  managed_zone = google_dns_managed_zone.internal.name
  type         = "A"
  ttl          = 300
  rrdatas      = ["10.80.1.10"]
}

resource "google_service_account" "app" {
  account_id   = local.name
  display_name = local.name
  description  = "infraharvest sandbox: the import must find this service account."
}

# A 2nd gen function from a zip of inline code, in the bucket above: the
# default selection must leave out the Cloud Run service it runs on and
# the gcf-v2-sources-<project number>-<region> bucket Cloud Functions
# copies its source to. Cloud Build builds it, as the bootstrap's
# infraharvest-ci-function, into the gcf-artifacts repository Cloud
# Functions creates in the region, and it runs as that account too, with
# no instances while it isn't called, which it never is: it costs nothing
# idle. Destroying it deletes the Cloud Run service; the sources bucket
# and the repository, which hold a few kilobytes and megabytes, stay for
# the next run.
#
# Its name is the same for every run, of either engine: Cloud Asset
# Inventory syncs functions every 7 hours, so it still lists one an
# earlier run deleted, which the import could only fail to import, unless
# it is this run's.
data "archive_file" "function" {
  type        = "zip"
  output_path = "${path.module}/function.zip"
  source {
    content  = "def hello(request):\n    return \"ok\"\n"
    filename = "main.py"
  }
}

resource "google_storage_bucket_object" "function" {
  name   = "function-${data.archive_file.function.output_md5}.zip"
  bucket = google_storage_bucket.data.name
  source = data.archive_file.function.output_path
}

locals {
  function_account = "infraharvest-ci-function@${var.project}.iam.gserviceaccount.com"
}

resource "google_cloudfunctions2_function" "app" {
  name        = "ih-sbx-function"
  location    = var.region
  description = "infraharvest sandbox"
  labels      = local.labels

  build_config {
    runtime         = "python312"
    entry_point     = "hello"
    service_account = "projects/${var.project}/serviceAccounts/${local.function_account}"

    source {
      storage_source {
        bucket = google_storage_bucket.data.name
        object = google_storage_bucket_object.function.name
      }
    }
  }

  service_config {
    available_memory      = "128Mi"
    min_instance_count    = 0
    max_instance_count    = 1
    ingress_settings      = "ALLOW_INTERNAL_ONLY"
    service_account_email = local.function_account
  }
}

# A Cloud Run service of Google's public sample image, reached through
# the load balancer below alone, with no instances while it isn't called,
# which it never is: it costs nothing idle. It runs as
# infraharvest-ci-function, which the fixture may act as. Unlike the
# function's, it carries no goog-managed-by label, so the default
# selection must import it.
resource "google_cloud_run_v2_service" "app" {
  name                = "${local.name}-run"
  location            = var.region
  description         = "infraharvest sandbox"
  ingress             = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
  deletion_protection = false
  labels              = local.labels

  template {
    service_account = local.function_account

    scaling {
      min_instance_count = 0
      max_instance_count = 1
    }

    containers {
      image = "us-docker.pkg.dev/cloudrun/container/hello"

      resources {
        cpu_idle = true
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
      }
    }
  }
}

# A global external Application Load Balancer: a serverless network
# endpoint group for the Cloud Run service behind a backend service, a
# backend bucket for the bucket above under /static/, a URL map, an HTTP
# proxy, and a forwarding rule on a reserved global address. Only the
# forwarding rule and the address bill by the hour, about $0.03 for both;
# nothing calls them.
resource "google_compute_region_network_endpoint_group" "run" {
  name                  = "${local.name}-run"
  region                = var.region
  network_endpoint_type = "SERVERLESS"

  cloud_run {
    service = google_cloud_run_v2_service.app.name
  }
}

resource "google_compute_backend_service" "run" {
  name                  = "${local.name}-run"
  description           = "infraharvest sandbox"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  protocol              = "HTTP"

  backend {
    group = google_compute_region_network_endpoint_group.run.id
  }
}

resource "google_compute_backend_bucket" "static" {
  name        = "${local.name}-static"
  description = "infraharvest sandbox"
  bucket_name = google_storage_bucket.data.name
}

resource "google_compute_url_map" "web" {
  name            = local.name
  description     = "infraharvest sandbox"
  default_service = google_compute_backend_service.run.id

  host_rule {
    hosts        = ["*"]
    path_matcher = "web"
  }

  path_matcher {
    name            = "web"
    default_service = google_compute_backend_service.run.id

    path_rule {
      paths   = ["/static/*"]
      service = google_compute_backend_bucket.static.id
    }
  }
}

resource "google_compute_target_http_proxy" "web" {
  name    = local.name
  url_map = google_compute_url_map.web.id
}

resource "google_compute_global_address" "web" {
  name        = local.name
  description = "infraharvest sandbox"
  labels      = local.labels
}

resource "google_compute_global_forwarding_rule" "web" {
  name                  = local.name
  target                = google_compute_target_http_proxy.web.id
  ip_address            = google_compute_global_address.web.id
  port_range            = "80"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  labels                = local.labels
}

# The smallest Cloud SQL for PostgreSQL instance: shared-core db-f1-micro,
# Enterprise edition, one zone, 10 GB of SSD, no backups, no deletion
# protection, and a public address with no authorized networks, so
# nothing reaches it. About $0.03 an hour with its storage and address;
# it takes 10 to 15 minutes to create and a few to delete. Its name can't
# be used again for a week after it is deleted, which the random suffix
# sees to. The import must import it and the app database, which the
# cloudsql service lists, and leave out the postgres database Cloud SQL
# creates. No lister lists users: the user is there for its password,
# which no file of the import may hold.
resource "google_sql_database_instance" "db" {
  name                = "${local.name}-db"
  region              = var.region
  database_version    = "POSTGRES_16"
  deletion_protection = false

  settings {
    edition                     = "ENTERPRISE"
    tier                        = "db-f1-micro"
    availability_type           = "ZONAL"
    disk_size                   = 10
    disk_autoresize             = false
    deletion_protection_enabled = false
    user_labels                 = local.labels

    backup_configuration {
      enabled = false
    }

    ip_configuration {
      ipv4_enabled = true
    }
  }
}

resource "google_sql_database" "app" {
  name     = "app"
  instance = google_sql_database_instance.db.name

  # Deleting the instance deletes it.
  deletion_policy = "ABANDON"
}

resource "random_password" "db" {
  length  = 24
  special = false
}

resource "google_sql_user" "app" {
  name     = "app"
  instance = google_sql_database_instance.db.name
  password = random_password.db.result

  # Deleting the instance deletes it; a PostgreSQL user that owns objects
  # can't be dropped on its own.
  deletion_policy = "ABANDON"
}

# A BigQuery dataset in the region with an empty table of two columns:
# nothing stored, nothing queried, nothing billed. Dataset IDs take no
# hyphens, so its name is the prefix's with underscores.
resource "google_bigquery_dataset" "data" {
  dataset_id                 = "ih_sbx_${var.suffix}_${random_string.id.result}"
  location                   = var.region
  description                = "infraharvest sandbox"
  delete_contents_on_destroy = true
  labels                     = local.labels
}

resource "google_bigquery_table" "events" {
  dataset_id          = google_bigquery_dataset.data.dataset_id
  table_id            = "events"
  description         = "infraharvest sandbox"
  deletion_protection = false
  labels              = local.labels

  schema = jsonencode([
    { name = "id", type = "STRING", mode = "REQUIRED" },
    { name = "at", type = "TIMESTAMP", mode = "NULLABLE" },
  ])
}

# A key ring with a symmetric software key. Neither can be deleted: the
# destroy forgets them, and schedules the key's one version for
# destruction after the shortest wait, a day, after which it bills
# nothing (a version bills $0.06 a month while it exists). So the project
# keeps a key ring and a key from every run, named after it, which later
# runs import too: the test doesn't wait for Cloud Asset Inventory to
# stop listing them, and the sweep destroys only the versions a failed
# run left enabled.
resource "google_kms_key_ring" "main" {
  name     = local.name
  location = var.region
}

resource "google_kms_crypto_key" "main" {
  name                       = local.name
  key_ring                   = google_kms_key_ring.main.id
  destroy_scheduled_duration = "86400s"
  labels                     = local.labels
}

# A secret with no version, so no value: free.
resource "google_secret_manager_secret" "app" {
  secret_id = local.name
  labels    = local.labels

  replication {
    auto {}
  }
}

# An empty Docker repository: free.
resource "google_artifact_registry_repository" "images" {
  repository_id = local.name
  location      = var.region
  format        = "DOCKER"
  description   = "infraharvest sandbox"
  labels        = local.labels
}

# A paused Cloud Scheduler job that would publish to the topic once a
# year: within the three free jobs a billing account has. Jobs take no
# labels, and Cloud Asset Inventory doesn't list them: the schedulerJobs
# service does.
resource "google_cloud_scheduler_job" "tick" {
  name        = local.name
  region      = var.region
  description = "infraharvest sandbox"
  schedule    = "0 0 1 1 *"
  time_zone   = "Etc/UTC"
  paused      = true

  pubsub_target {
    topic_name = google_pubsub_topic.events.id
    data       = base64encode("tick")
  }
}
