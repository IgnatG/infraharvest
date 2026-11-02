#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The sandbox-google workflow's sweep, the round trip's last step after
# the estate's destroy, whatever happened, and the scheduled clean-up's
# (sandbox-leftovers.yml): deletes what Terraform's state doesn't
# hold, such as a resource whose creation failed half-way, or what a run
# whose destroy never finished left: every resource of a sandbox estate,
# whichever engine's run made it (runs never overlap). Networks,
# subnetworks, firewall rules, firewall policies, routers and service
# accounts take no labels, so the sweep finds everything by name,
# ih-sbx-<engine>-, which the estate gives every resource but its
# function, ih-sbx-function, and its BigQuery dataset, ih_sbx_<engine>_,
# and deletes what uses others first: the function, the load balancer
# (forwarding rules, proxies, URL maps, backend services and buckets,
# network endpoint groups, addresses), the Cloud Run services, the
# scheduler jobs, the Cloud SQL instances, the datasets, the secrets and
# the Artifact Registry repositories, the zones' record sets, the zones,
# the subscriptions and topics, the buckets, the instances and their
# disks, then the network's firewall rules, the network firewall policy,
# which no network uses, the routers and subnetworks, the network, and
# the service account. KMS key rings and keys can't be deleted: it
# schedules the destruction of their versions still enabled or disabled,
# and leaves them. It fails if anything else is left.
#
# The estates' resource types this sweep deletes, or that go with what
# holds them. sandbox-sweep-coverage.sh fails a pull request whose
# estates (e2e/testdata/sandbox/google*) use a type listed in neither:
# a new kind needs its own delete here, in the order its dependencies
# allow, and in the check of what is left at the end.
#
# sweep-coverage swept: google_artifact_registry_repository google_bigquery_dataset
# sweep-coverage swept: google_cloud_run_v2_service google_cloud_scheduler_job google_cloudfunctions2_function
# sweep-coverage swept: google_compute_backend_bucket google_compute_backend_service google_compute_disk
# sweep-coverage swept: google_compute_firewall google_compute_global_address google_compute_global_forwarding_rule
# sweep-coverage swept: google_compute_instance google_compute_network google_compute_network_firewall_policy
# sweep-coverage swept: google_compute_region_network_endpoint_group google_compute_router
# sweep-coverage swept: google_compute_subnetwork google_compute_target_http_proxy google_compute_url_map
# sweep-coverage swept: google_dns_managed_zone google_dns_record_set google_kms_crypto_key google_kms_key_ring
# sweep-coverage swept: google_pubsub_subscription google_pubsub_topic google_secret_manager_secret
# sweep-coverage swept: google_service_account google_sql_database_instance google_storage_bucket
# sweep-coverage with-parent: google_bigquery_table google_sql_database google_sql_user
# sweep-coverage with-parent: google_storage_bucket_object
#
# Usage: CLOUDSDK_CORE_PROJECT=<project> [SANDBOX_GOOGLE_REGION=<region>]
# sandbox-google-sweep.sh. SANDBOX_GOOGLE_REGION names the region of the
# estate's regional resources, by default europe-west2. Signed in to
# gcloud as the fixture service account
# (CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE).
set -euo pipefail

project=${CLOUDSDK_CORE_PROJECT:?}
region=${SANDBOX_GOOGLE_REGION:-europe-west2}
prefix="ih-sbx-"
named="name ~ '^$prefix'"
q=(--quiet)

# listed runs a gcloud list command and prints what it lists. A project
# where a service's API isn't enabled holds none of its resources, so that
# failure lists nothing; any other failure stops the sweep.
listed() {
  local out err
  err=$(mktemp)
  if out=$(gcloud "$@" 2>"$err"); then
    rm -f "$err"
    printf '%s\n' "$out"
    return
  fi
  if grep -qE 'SERVICE_DISABLED|has not been used in project' "$err"; then
    rm -f "$err"
    return 0
  fi
  cat "$err" >&2
  rm -f "$err"
  return 1
}
functions=$(listed functions list --filter "name ~ '/functions/ih-sbx-'" --format 'value(name.basename(),name.segment(3))')
while read -r name function_region; do
  if [ -n "$name" ]; then
    gcloud functions delete "$name" --region "$function_region" "${q[@]}" || true
  fi
done <<< "$functions"
# The load balancer, from its forwarding rules to the backends and the
# address, before the Cloud Run service and the bucket it serves.
for kind in forwarding-rules target-http-proxies url-maps backend-services; do
  names=$(listed compute "$kind" list --global --filter "$named" --format 'value(name)')
  while IFS= read -r name; do
    if [ -n "$name" ]; then
      gcloud compute "$kind" delete "$name" --global "${q[@]}" || true
    fi
  done <<< "$names"
done
names=$(listed compute backend-buckets list --filter "$named" --format 'value(name)')
while IFS= read -r name; do
  if [ -n "$name" ]; then
    gcloud compute backend-buckets delete "$name" "${q[@]}" || true
  fi
done <<< "$names"
negs=$(listed compute network-endpoint-groups list --filter "$named" --format 'value(name,region.basename())')
while read -r name neg_region; do
  if [ -n "$name" ] && [ -n "$neg_region" ]; then
    gcloud compute network-endpoint-groups delete "$name" --region "$neg_region" "${q[@]}" || true
  fi
done <<< "$negs"
names=$(listed compute addresses list --global --filter "$named" --format 'value(name)')
while IFS= read -r name; do
  if [ -n "$name" ]; then
    gcloud compute addresses delete "$name" --global "${q[@]}" || true
  fi
done <<< "$names"
# The Cloud Run services, scheduler jobs, repositories and key rings of
# the estate are in its region.
names=$(listed run services list --region "$region" --filter "metadata.name ~ '^$prefix'" --format 'value(metadata.name)')
while IFS= read -r name; do
  if [ -n "$name" ]; then
    gcloud run services delete "$name" --region "$region" "${q[@]}" || true
  fi
done <<< "$names"
names=$(listed scheduler jobs list --location "$region" --filter "name ~ '/jobs/$prefix'" --format 'value(name.basename())')
while IFS= read -r name; do
  if [ -n "$name" ]; then
    gcloud scheduler jobs delete "$name" --location "$region" "${q[@]}" || true
  fi
done <<< "$names"
names=$(listed sql instances list --filter "$named" --format 'value(name)')
while IFS= read -r name; do
  if [ -n "$name" ]; then
    gcloud sql instances delete "$name" "${q[@]}" || true
  fi
done <<< "$names"
# BigQuery dataset IDs take no hyphens: the estate's start ih_sbx_. The
# REST API, with gcloud's token, as bq signs in apart.
bigquery="https://bigquery.googleapis.com/bigquery/v2/projects/$project/datasets"
token=$(gcloud auth print-access-token)
datasets=$(curl -fsS -H "Authorization: Bearer $token" "$bigquery?all=true&maxResults=1000" |
  jq -r '.datasets[]?.datasetReference.datasetId' | grep '^ih_sbx_' || true)
while IFS= read -r dataset; do
  if [ -n "$dataset" ]; then
    curl -fsS -X DELETE -H "Authorization: Bearer $token" "$bigquery/$dataset?deleteContents=true" || true
  fi
done <<< "$datasets"
names=$(listed secrets list --filter "name ~ '/secrets/$prefix'" --format 'value(name.basename())')
while IFS= read -r name; do
  if [ -n "$name" ]; then
    gcloud secrets delete "$name" "${q[@]}" || true
  fi
done <<< "$names"
names=$(listed artifacts repositories list --location "$region" --filter "name ~ '/repositories/$prefix'" --format 'value(name.basename())')
while IFS= read -r name; do
  if [ -n "$name" ]; then
    gcloud artifacts repositories delete "$name" --location "$region" "${q[@]}" || true
  fi
done <<< "$names"
# Key rings and keys can't be deleted: every run leaves its own. Only the
# versions of their keys still enabled or disabled, as a run whose
# destroy didn't finish leaves them, are scheduled for destruction.
rings=$(listed kms keyrings list --location "$region" --filter "name ~ '/keyRings/$prefix'" --format 'value(name)')
while IFS= read -r ring; do
  if [ -z "$ring" ]; then
    continue
  fi
  keys=$(listed kms keys list --keyring "$ring" --format 'value(name)')
  while IFS= read -r key; do
    if [ -z "$key" ]; then
      continue
    fi
    versions=$(listed kms keys versions list --key "$key" --filter 'state=ENABLED OR state=DISABLED' --format 'value(name)')
    while IFS= read -r version; do
      if [ -n "$version" ]; then
        gcloud kms keys versions destroy "$version" "${q[@]}" || true
      fi
    done <<< "$versions"
  done <<< "$keys"
done <<< "$rings"
zones=$(listed dns managed-zones list --filter "$named" --format 'value(name)')
while IFS= read -r zone; do
  if [ -z "$zone" ]; then
    continue
  fi
  sets=$(listed dns record-sets list --zone "$zone" --filter 'type != SOA AND type != NS' --format 'csv[no-heading](name,type)')
  while IFS=, read -r name type; do
    if [ -n "$name" ]; then
      gcloud dns record-sets delete "$name" --type "$type" --zone "$zone" "${q[@]}" || true
    fi
  done <<< "$sets"
  gcloud dns managed-zones delete "$zone" "${q[@]}" || true
done <<< "$zones"
for kind in subscriptions topics; do
  names=$(listed pubsub "$kind" list --filter "name ~ '/$prefix'" --format 'value(name.basename())')
  while IFS= read -r name; do
    if [ -n "$name" ]; then
      gcloud pubsub "$kind" delete "$name" "${q[@]}" || true
    fi
  done <<< "$names"
done
buckets=$(listed storage buckets list --filter "$named" --format 'value(name)')
while IFS= read -r bucket; do
  if [ -n "$bucket" ]; then
    gcloud storage rm --recursive "gs://$bucket" "${q[@]}" || true
  fi
done <<< "$buckets"
# The instances, with their boot disks, before the data disks they
# attach and the subnetworks they use.
for kind in instances disks; do
  found=$(listed compute "$kind" list --filter "$named" --format 'value(name,zone.basename())')
  while read -r name zone; do
    if [ -n "$name" ]; then
      gcloud compute "$kind" delete "$name" --zone "$zone" "${q[@]}" || true
    fi
  done <<< "$found"
done
rules=$(listed compute firewall-rules list --filter "$named" --format 'value(name)')
while IFS= read -r rule; do
  if [ -n "$rule" ]; then
    gcloud compute firewall-rules delete "$rule" "${q[@]}" || true
  fi
done <<< "$rules"
policies=$(listed compute network-firewall-policies list --filter "$named" --format 'value(name)')
while IFS= read -r policy; do
  if [ -n "$policy" ]; then
    gcloud compute network-firewall-policies delete "$policy" --global "${q[@]}" || true
  fi
done <<< "$policies"
for kind in routers "networks subnets"; do
  read -r -a group <<< "$kind"
  found=$(listed compute "${group[@]}" list --filter "$named" --format 'value(name,region.basename())')
  while read -r name subnet_region; do
    if [ -n "$name" ]; then
      gcloud compute "${group[@]}" delete "$name" --region "$subnet_region" "${q[@]}" || true
    fi
  done <<< "$found"
done
networks=$(listed compute networks list --filter "$named" --format 'value(name)')
while IFS= read -r network; do
  if [ -n "$network" ]; then
    gcloud compute networks delete "$network" "${q[@]}" || true
  fi
done <<< "$networks"
accounts=$(listed iam service-accounts list --filter "email ~ '^$prefix'" --format 'value(email)')
while IFS= read -r account; do
  if [ -n "$account" ]; then
    gcloud iam service-accounts delete "$account" "${q[@]}" || true
  fi
done <<< "$accounts"
left=$(
  listed functions list --filter "name ~ '/functions/ih-sbx-'" --format 'value(name)'
  for kind in forwarding-rules target-http-proxies url-maps backend-services addresses; do
    listed compute "$kind" list --global --filter "$named" --format 'value(name)'
  done
  listed compute backend-buckets list --filter "$named" --format 'value(name)'
  listed compute network-endpoint-groups list --filter "$named" --format 'value(name)'
  listed run services list --region "$region" --filter "metadata.name ~ '^$prefix'" --format 'value(metadata.name)'
  listed scheduler jobs list --location "$region" --filter "name ~ '/jobs/$prefix'" --format 'value(name)'
  listed sql instances list --filter "$named" --format 'value(name)'
  curl -fsS -H "Authorization: Bearer $token" "$bigquery?all=true&maxResults=1000" |
    jq -r '.datasets[]?.datasetReference.datasetId' | grep '^ih_sbx_' || true
  listed secrets list --filter "name ~ '/secrets/$prefix'" --format 'value(name)'
  listed artifacts repositories list --location "$region" --filter "name ~ '/repositories/$prefix'" --format 'value(name)'
  listed dns managed-zones list --filter "$named" --format 'value(name)'
  listed pubsub topics list --filter "name ~ '/$prefix'" --format 'value(name)'
  listed pubsub subscriptions list --filter "name ~ '/$prefix'" --format 'value(name)'
  listed storage buckets list --filter "$named" --format 'value(name)'
  listed compute instances list --filter "$named" --format 'value(name)'
  listed compute disks list --filter "$named" --format 'value(name)'
  listed compute firewall-rules list --filter "$named" --format 'value(name)'
  listed compute network-firewall-policies list --filter "$named" --format 'value(name)'
  listed compute routers list --filter "$named" --format 'value(name)'
  listed compute networks subnets list --filter "$named" --format 'value(name)'
  listed compute networks list --filter "$named" --format 'value(name)'
  listed iam service-accounts list --filter "email ~ '^$prefix'" --format 'value(email)'
)
if [ -n "$left" ]; then
  echo "::error::Left in $project: $left"
  exit 1
fi
