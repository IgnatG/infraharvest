#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The sandbox-aws workflow's last resort, after the estate's destroy: finds
# what is still tagged infraharvest-sandbox-run=$RUN_ID in each region,
# deletes the types that cost the most while they exist (EC2 instances, NAT
# gateways and their Elastic IPs, RDS instances and clusters, Redshift
# clusters, ElastiCache replication groups, EKS clusters, load balancers,
# Kinesis streams, Lambda functions and S3 buckets), without final
# snapshots, and warns about whatever is still tagged. It touches nothing
# without this run's tag. Deletes take a while, and the Resource Groups
# Tagging API lists some deleted resources for a while too, so what it
# warns about may already be going. It prints names and ARNs only.
#
# Usage: RUN_ID=<run id> sandbox-aws-sweep.sh <region>...
# Not -e: one failed call mustn't stop the sweep of the rest.
set -uo pipefail

key=infraharvest-sandbox-run
# An empty value would match every value of the key.
run=${RUN_ID:?RUN_ID names the run whose resources to sweep}
if [ "$#" -eq 0 ]; then
  echo "usage: RUN_ID=<run id> $0 <region>..." >&2
  exit 2
fi

# tagged prints the ARNs in region $1 tagged with this run, one per line.
tagged() {
  aws resourcegroupstaggingapi get-resources --region "$1" \
    --tag-filters "Key=$key,Values=$run" \
    --query 'ResourceTagMappingList[].ResourceARN' --output text | tr '\t' '\n' | sed '/^$/d'
}

# try runs a delete, and says so rather than stopping when it fails, as it
# does on a resource already being deleted.
try() {
  if ! "$@" > /dev/null; then
    echo "failed: $*"
  fi
}

# words prints the words of $1, one per line: the AWS CLI's text output
# separates them with tabs.
words() {
  tr '\t' '\n' <<< "$1" | sed '/^$/d'
}

# empty_bucket deletes every object version and delete marker in bucket $1,
# in region $2, a thousand at a time.
empty_bucket() {
  local batch
  for _ in $(seq 1000); do
    batch=$(aws s3api list-object-versions --region "$2" --bucket "$1" --max-items 1000 \
      --query '{Objects: [Versions[], DeleteMarkers[]][].{Key: Key, VersionId: VersionId}}' \
      --output json) || return 1
    if [ "$(jq '.Objects | length' <<< "$batch")" -eq 0 ]; then
      return 0
    fi
    aws s3api delete-objects --region "$2" --bucket "$1" --delete "$batch" > /dev/null || return 1
  done
}

# Each region once.
mapfile -t regions < <(printf '%s\n' "$@" | sed '/^$/d' | sort -u)
for region in "${regions[@]}"; do
  echo "== $region"
  # EC2 from its own API, by state: the Tagging API lists terminated
  # instances and deleted NAT gateways for a while.
  instances=$(aws ec2 describe-instances --region "$region" \
    --filters "Name=tag:$key,Values=$run" Name=instance-state-name,Values=pending,running,stopping,stopped \
    --query 'Reservations[].Instances[].InstanceId' --output text)
  if [ -n "$instances" ]; then
    mapfile -t ids < <(words "$instances")
    echo "terminating instances: ${ids[*]}"
    try aws ec2 terminate-instances --region "$region" --instance-ids "${ids[@]}"
  fi
  mapfile -t nats < <(words "$(aws ec2 describe-nat-gateways --region "$region" \
    --filter "Name=tag:$key,Values=$run" Name=state,Values=pending,available \
    --query 'NatGateways[].NatGatewayId' --output text)")
  for nat in "${nats[@]}"; do
    echo "deleting NAT gateway $nat"
    try aws ec2 delete-nat-gateway --region "$region" --nat-gateway-id "$nat"
  done

  rds_instances=()
  rds_clusters=()
  eks_clusters=()
  while read -r arn <&3; do
    # arn:partition:service:region:account:resource, where the resource
    # may hold more colons.
    IFS=: read -r _ _ service _ _ resource <<< "$arn"
    case "$service:$resource" in
      rds:db:*) rds_instances+=("${resource#db:}") ;;
      rds:cluster:*) rds_clusters+=("${resource#cluster:}") ;;
      eks:cluster/*) eks_clusters+=("${resource#cluster/}") ;;
      redshift:cluster:*)
        echo "deleting Redshift cluster ${resource#cluster:}"
        try aws redshift delete-cluster --region "$region" --cluster-identifier "${resource#cluster:}" \
          --skip-final-cluster-snapshot
        ;;
      elasticache:replicationgroup:*)
        echo "deleting ElastiCache replication group ${resource#replicationgroup:}"
        try aws elasticache delete-replication-group --region "$region" \
          --replication-group-id "${resource#replicationgroup:}"
        ;;
      elasticloadbalancing:loadbalancer/app/* | elasticloadbalancing:loadbalancer/net/* | elasticloadbalancing:loadbalancer/gwy/*)
        echo "deleting load balancer $arn"
        try aws elbv2 delete-load-balancer --region "$region" --load-balancer-arn "$arn"
        ;;
      elasticloadbalancing:loadbalancer/*)
        echo "deleting classic load balancer ${resource#loadbalancer/}"
        try aws elb delete-load-balancer --region "$region" --load-balancer-name "${resource#loadbalancer/}"
        ;;
      kinesis:stream/*)
        echo "deleting Kinesis stream ${resource#stream/}"
        try aws kinesis delete-stream --region "$region" --stream-name "${resource#stream/}" --enforce-consumer-deletion
        ;;
      lambda:function:*)
        name=${resource#function:}
        echo "deleting Lambda function ${name%%:*}"
        try aws lambda delete-function --region "$region" --function-name "${name%%:*}"
        ;;
      s3:*)
        echo "emptying and deleting S3 bucket $resource"
        if empty_bucket "$resource" "$region"; then
          try aws s3api delete-bucket --region "$region" --bucket "$resource"
        else
          echo "failed to empty $resource"
        fi
        ;;
    esac
  done 3< <(tagged "$region")

  for db in "${rds_instances[@]}"; do
    echo "deleting RDS instance $db"
    try aws rds delete-db-instance --region "$region" --db-instance-identifier "$db" \
      --skip-final-snapshot --delete-automated-backups
  done
  # A cluster goes once its instances have.
  if [ "${#rds_clusters[@]}" -gt 0 ]; then
    for db in "${rds_instances[@]}"; do
      try aws rds wait db-instance-deleted --region "$region" --db-instance-identifier "$db"
    done
    for cluster in "${rds_clusters[@]}"; do
      echo "deleting RDS cluster $cluster"
      try aws rds delete-db-cluster --region "$region" --db-cluster-identifier "$cluster" --skip-final-snapshot
    done
  fi
  # An EKS cluster goes once its node groups have, whatever their tags.
  for cluster in "${eks_clusters[@]}"; do
    mapfile -t nodegroups < <(words "$(aws eks list-nodegroups --region "$region" --cluster-name "$cluster" \
      --query 'nodegroups[]' --output text)")
    for nodegroup in "${nodegroups[@]}"; do
      echo "deleting EKS node group $cluster/$nodegroup"
      try aws eks delete-nodegroup --region "$region" --cluster-name "$cluster" --nodegroup-name "$nodegroup"
    done
    for nodegroup in "${nodegroups[@]}"; do
      try aws eks wait nodegroup-deleted --region "$region" --cluster-name "$cluster" --nodegroup-name "$nodegroup"
    done
    echo "deleting EKS cluster $cluster"
    try aws eks delete-cluster --region "$region" --name "$cluster"
  done
  # An Elastic IP is released once its NAT gateway is gone.
  if [ "${#nats[@]}" -gt 0 ]; then
    for _ in $(seq 60); do
      pending=$(aws ec2 describe-nat-gateways --region "$region" --nat-gateway-ids "${nats[@]}" \
        --query "NatGateways[?State!='deleted'].NatGatewayId" --output text)
      [ -z "$pending" ] && break
      sleep 10
    done
  fi
  mapfile -t eips < <(words "$(aws ec2 describe-addresses --region "$region" --filters "Name=tag:$key,Values=$run" \
    --query 'Addresses[].AllocationId' --output text)")
  for eip in "${eips[@]}"; do
    echo "releasing Elastic IP $eip"
    try aws ec2 release-address --region "$region" --allocation-id "$eip"
  done

  left=$(tagged "$region" | tr '\n' ' ')
  if [ -n "$left" ]; then
    echo "::warning::Still tagged $key=$run in $region after the sweep (deletes may still be running, and the Tagging API lists deleted resources for a while): $left"
  fi
done
