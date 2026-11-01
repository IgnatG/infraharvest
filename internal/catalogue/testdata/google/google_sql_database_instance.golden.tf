# google_sql_database_instance, from data/google/data.yaml

import {
  to = google_sql_database_instance.billing_db
  id = "projects/acme-prod/instances/billing-db"
}

import {
  to = google_sql_database_instance.orders_db
  id = "projects/acme-prod/instances/orders-db"
}

# Left out of generated configuration: deletion_policy=DELETE, settings.ip_configuration.server_certificate_rotation_mode={*}_UNSPECIFIED

# Generated configuration, without the arguments the catalogue omits:
resource "google_sql_database_instance" "orders_db" {
  database_version = "POSTGRES_16"
  name             = "orders-db"
  project          = "acme-prod"
  region           = "europe-west2"
  settings {
    tier = "db-custom-1-3840"
    ip_configuration {
      ipv4_enabled = true
    }
  }
}

resource "google_sql_database_instance" "billing_db" {
  database_version = "POSTGRES_16"
  name             = "billing-db"
  project          = "acme-prod"
  region           = "europe-west2"
  settings {
    tier = "db-custom-1-3840"
    ip_configuration {
      ipv4_enabled                     = true
      server_certificate_rotation_mode = "AUTOMATIC_ROTATION_DURING_MAINTENANCE"
    }
  }
}
