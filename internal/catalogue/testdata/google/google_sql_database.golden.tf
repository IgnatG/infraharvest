# google_sql_database, from data/google/cloudsql.yaml

import {
  to = google_sql_database.billing_ledger
  id = "projects/acme-prod/instances/billing/databases/ledger"
}

import {
  to = google_sql_database.billing_sys
  id = "projects/acme-prod/instances/billing/databases/sys"
}

import {
  to = google_sql_database.crm_crm
  id = "projects/acme-prod/instances/crm/databases/crm"
}

import {
  to = google_sql_database.orders_app
  id = "projects/acme-prod/instances/orders/databases/app"
}

# projects/acme-prod/instances/orders/databases/mysql: left out by default, would import as "projects/acme-prod/instances/orders/databases/mysql": a system database of MySQL, in every MySQL instance
# projects/acme-prod/instances/orders/databases/information_schema: left out by default, would import as "projects/acme-prod/instances/orders/databases/information_schema": a system database of MySQL, in every MySQL instance
# projects/acme-prod/instances/orders/databases/performance_schema: left out by default, would import as "projects/acme-prod/instances/orders/databases/performance_schema": a system database of MySQL, in every MySQL instance
# projects/acme-prod/instances/orders/databases/sys: left out by default, would import as "projects/acme-prod/instances/orders/databases/sys": a system database of MySQL, in every MySQL instance
# projects/acme-prod/instances/billing/databases/postgres: left out by default, would import as "projects/acme-prod/instances/billing/databases/postgres": the default database Cloud SQL creates in every PostgreSQL instance; select it to import it
# projects/acme-prod/instances/billing/databases/cloudsqladmin: left out by default, would import as "projects/acme-prod/instances/billing/databases/cloudsqladmin": Cloud SQL's own database for managing a PostgreSQL instance
# projects/acme-prod/instances/crm/databases/master: left out by default, would import as "projects/acme-prod/instances/crm/databases/master": a system database of SQL Server, in every SQL Server instance
# projects/acme-prod/instances/crm/databases/model: left out by default, would import as "projects/acme-prod/instances/crm/databases/model": a system database of SQL Server, in every SQL Server instance
# projects/acme-prod/instances/crm/databases/msdb: left out by default, would import as "projects/acme-prod/instances/crm/databases/msdb": a system database of SQL Server, in every SQL Server instance
# projects/acme-prod/instances/crm/databases/tempdb: left out by default, would import as "projects/acme-prod/instances/crm/databases/tempdb": a system database of SQL Server, in every SQL Server instance
