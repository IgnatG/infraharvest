#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The sandbox-google workflow's sweep, which its round trip runs when it
# failed or found leftovers, and its clean-up job when the round trip was
# cancelled, timed out or failed: deletes what Terraform's state doesn't
# hold, such as a resource whose creation failed half-way, or what a run
# whose destroy never finished left: every resource of a sandbox estate,
# whichever engine's run made it (runs never overlap). Networks,
# subnetworks, firewall rules, firewall policies, routers and service
# accounts take no labels, so the sweep finds everything by name,
# ih-sbx-<engine>-, which the estate gives every resource but its
# function, ih-sbx-function, and deletes what uses others first: the
# function, the zones' record sets, the zones, the subscriptions and
# topics, the buckets, the instances and their disks, then the network's
# firewall rules, the network firewall policy, which no network uses, the
# routers and subnetworks, the network, and the service account. It fails
# if any of them is left.
#
# Usage: CLOUDSDK_CORE_PROJECT=<project> sandbox-google-sweep.sh
# Signed in to gcloud as the fixture service account
# (CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE).
set -euo pipefail

project=${CLOUDSDK_CORE_PROJECT:?}
prefix="ih-sbx-"
named="name ~ '^$prefix'"
q=(--quiet)
functions=$(gcloud functions list --filter "name ~ '/functions/ih-sbx-'" --format 'value(name.basename(),name.segment(3))')
while read -r name region; do
  if [ -n "$name" ]; then
    gcloud functions delete "$name" --region "$region" "${q[@]}" || true
  fi
done <<< "$functions"
zones=$(gcloud dns managed-zones list --filter "$named" --format 'value(name)')
while IFS= read -r zone; do
  if [ -z "$zone" ]; then
    continue
  fi
  sets=$(gcloud dns record-sets list --zone "$zone" --filter 'type != SOA AND type != NS' --format 'csv[no-heading](name,type)')
  while IFS=, read -r name type; do
    if [ -n "$name" ]; then
      gcloud dns record-sets delete "$name" --type "$type" --zone "$zone" "${q[@]}" || true
    fi
  done <<< "$sets"
  gcloud dns managed-zones delete "$zone" "${q[@]}" || true
done <<< "$zones"
for kind in subscriptions topics; do
  names=$(gcloud pubsub "$kind" list --filter "name ~ '/$prefix'" --format 'value(name.basename())')
  while IFS= read -r name; do
    if [ -n "$name" ]; then
      gcloud pubsub "$kind" delete "$name" "${q[@]}" || true
    fi
  done <<< "$names"
done
buckets=$(gcloud storage buckets list --filter "$named" --format 'value(name)')
while IFS= read -r bucket; do
  if [ -n "$bucket" ]; then
    gcloud storage rm --recursive "gs://$bucket" "${q[@]}" || true
  fi
done <<< "$buckets"
# The instances, with their boot disks, before the data disks they
# attach and the subnetworks they use.
for kind in instances disks; do
  found=$(gcloud compute "$kind" list --filter "$named" --format 'value(name,zone.basename())')
  while read -r name zone; do
    if [ -n "$name" ]; then
      gcloud compute "$kind" delete "$name" --zone "$zone" "${q[@]}" || true
    fi
  done <<< "$found"
done
rules=$(gcloud compute firewall-rules list --filter "$named" --format 'value(name)')
while IFS= read -r rule; do
  if [ -n "$rule" ]; then
    gcloud compute firewall-rules delete "$rule" "${q[@]}" || true
  fi
done <<< "$rules"
policies=$(gcloud compute network-firewall-policies list --filter "$named" --format 'value(name)')
while IFS= read -r policy; do
  if [ -n "$policy" ]; then
    gcloud compute network-firewall-policies delete "$policy" --global "${q[@]}" || true
  fi
done <<< "$policies"
for kind in routers "networks subnets"; do
  read -r -a group <<< "$kind"
  found=$(gcloud compute "${group[@]}" list --filter "$named" --format 'value(name,region.basename())')
  while read -r name region; do
    if [ -n "$name" ]; then
      gcloud compute "${group[@]}" delete "$name" --region "$region" "${q[@]}" || true
    fi
  done <<< "$found"
done
networks=$(gcloud compute networks list --filter "$named" --format 'value(name)')
while IFS= read -r network; do
  if [ -n "$network" ]; then
    gcloud compute networks delete "$network" "${q[@]}" || true
  fi
done <<< "$networks"
accounts=$(gcloud iam service-accounts list --filter "email ~ '^$prefix'" --format 'value(email)')
while IFS= read -r account; do
  if [ -n "$account" ]; then
    gcloud iam service-accounts delete "$account" "${q[@]}" || true
  fi
done <<< "$accounts"
left=$(
  gcloud functions list --filter "name ~ '/functions/ih-sbx-'" --format 'value(name)'
  gcloud dns managed-zones list --filter "$named" --format 'value(name)'
  gcloud pubsub topics list --filter "name ~ '/$prefix'" --format 'value(name)'
  gcloud pubsub subscriptions list --filter "name ~ '/$prefix'" --format 'value(name)'
  gcloud storage buckets list --filter "$named" --format 'value(name)'
  gcloud compute instances list --filter "$named" --format 'value(name)'
  gcloud compute disks list --filter "$named" --format 'value(name)'
  gcloud compute firewall-rules list --filter "$named" --format 'value(name)'
  gcloud compute network-firewall-policies list --filter "$named" --format 'value(name)'
  gcloud compute routers list --filter "$named" --format 'value(name)'
  gcloud compute networks subnets list --filter "$named" --format 'value(name)'
  gcloud compute networks list --filter "$named" --format 'value(name)'
  gcloud iam service-accounts list --filter "email ~ '^$prefix'" --format 'value(email)'
)
if [ -n "$left" ]; then
  echo "::error::Left in $project: $left"
  exit 1
fi
