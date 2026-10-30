resource "google_sql_database_instance" "orders_db" {
  database_version = "POSTGRES_16"
  name             = "orders-db"
  project          = "acme-prod"
  region           = "europe-west2"
  settings {
    tier = "db-custom-1-3840"
    ip_configuration {
      ipv4_enabled                     = true
      server_certificate_rotation_mode = "SERVER_CERTIFICATE_ROTATION_MODE_UNSPECIFIED"
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
